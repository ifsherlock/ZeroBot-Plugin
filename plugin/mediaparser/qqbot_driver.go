package mediaparser

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"image"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RomiChan/websocket"
	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	qqBotAPIBase   = "https://api.sgroup.qq.com"
	qqBotTokenURL  = "https://bots.qq.com/app/getAppAccessToken"
	qqBotIntents   = 1 << 25
	qqBotUserAgent = "ZeroBot-Plugin/QQBot"

	qqBotMediaTypeImage     = 1
	qqBotMediaTypeVideo     = 2
	qqBotMediaMaxUploadSize = 10 << 20
)

var qqBotCQCodePattern = regexp.MustCompile(`\[CQ:([^,\]]+)([^\]]*)\]`)

type qqBotDriver struct {
	appID          string
	appSecret      string
	defaultOpenID  string
	defaultGroupID string
	name           string
	useMarkdown    bool
	publicBaseURL  string

	mu              sync.Mutex
	conn            *websocket.Conn
	client          *http.Client
	token           string
	tokenExpiresAt  time.Time
	selfID          int64
	lastSeq         any
	heartbeatCancel context.CancelFunc
	targetMu        sync.RWMutex
	targets         map[int64]qqBotTarget
}

type qqBotTarget struct {
	openID string
	group  bool
}

type qqBotMediaAttachment struct {
	fileType int
	target   string
}

// NewQQBotDriver creates the optional official QQBot driver from system settings.
func NewQQBotDriver(settings SystemSettings) (zero.Driver, bool) {
	settings = normalizeSystemSettings(settings)
	if !settings.QQBotEnabled || settings.QQBotAppID == "" || settings.QQBotSecret == "" {
		return nil, false
	}
	name := firstNonEmpty(settings.QQBotName, "qqbot")
	return &qqBotDriver{
		appID:          settings.QQBotAppID,
		appSecret:      settings.QQBotSecret,
		defaultOpenID:  settings.QQBotOpenID,
		defaultGroupID: settings.QQBotGroupOpenID,
		name:           name,
		useMarkdown:    settings.QQBotMarkdown,
		publicBaseURL:  settings.QQBotPublicBase,
		client:         &http.Client{Timeout: 30 * time.Second},
		selfID:         qqBotStableID("self:" + settings.QQBotAppID),
		targets:        map[int64]qqBotTarget{},
	}, true
}

func (d *qqBotDriver) Connect() {
	for {
		token, err := d.accessToken()
		if err != nil {
			logrus.Warnf("[qqbot] get token failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		gatewayURL, err := d.gatewayURL(token)
		if err != nil {
			logrus.Warnf("[qqbot] get gateway failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		header := http.Header{
			"Authorization": []string{"QQBot " + token},
			"User-Agent":    []string{qqBotUserAgent},
		}
		conn, _, err := websocket.DefaultDialer.Dial(gatewayURL, header)
		if err != nil {
			logrus.Warnf("[qqbot] connect gateway failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		d.mu.Lock()
		if d.heartbeatCancel != nil {
			d.heartbeatCancel()
			d.heartbeatCancel = nil
		}
		d.conn = conn
		d.mu.Unlock()
		zero.APICallers.Store(d.selfID, d)
		logrus.Infof("[qqbot] connected gateway name=%s self=%d", d.name, d.selfID)
		return
	}
}

func (d *qqBotDriver) Listen(handler func([]byte, zero.APICaller)) {
	for {
		d.mu.Lock()
		conn := d.conn
		d.mu.Unlock()
		if conn == nil {
			d.Connect()
			continue
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			logrus.Warnf("[qqbot] gateway disconnected: %v", err)
			zero.APICallers.Delete(d.selfID)
			d.Connect()
			continue
		}
		d.handleGatewayPayload(payload, handler)
	}
}

func (d *qqBotDriver) CallAPI(ctx context.Context, req zero.APIRequest) (zero.APIResponse, error) {
	switch req.Action {
	case "send_private_msg":
		userID := int64Param(req.Params, "user_id")
		target, ok := d.targetFor(userID, false)
		if !ok {
			return qqBotAPIError("qqbot private target not known"), nil
		}
		return d.sendOfficialMessage(ctx, target.openID, false, req.Params["message"])
	case "send_group_msg":
		groupID := int64Param(req.Params, "group_id")
		target, ok := d.targetFor(groupID, true)
		if !ok {
			return qqBotAPIError("qqbot group target not known"), nil
		}
		return d.sendOfficialMessage(ctx, target.openID, true, req.Params["message"])
	case "send_private_forward_msg":
		userID := int64Param(req.Params, "user_id")
		target, ok := d.targetFor(userID, false)
		if !ok {
			return qqBotAPIError("qqbot private target not known"), nil
		}
		return d.sendOfficialMessage(ctx, target.openID, false, d.forwardMessageText(req.Params["messages"]))
	case "send_group_forward_msg":
		groupID := int64Param(req.Params, "group_id")
		target, ok := d.targetFor(groupID, true)
		if !ok {
			return qqBotAPIError("qqbot group target not known"), nil
		}
		return d.sendOfficialMessage(ctx, target.openID, true, d.forwardMessageText(req.Params["messages"]))
	case "get_login_info":
		return qqBotAPIOK(map[string]any{"user_id": d.selfID, "nickname": d.name}), nil
	case "mark_msg_as_read":
		messageID := int64Param(req.Params, "message_id")
		logrus.Debugf("[qqbot] ignore mark_msg_as_read message_id=%d", messageID)
		return qqBotAPIOK(map[string]any{"message_id": messageID}), nil
	default:
		logrus.Infof("[qqbot] unsupported api action=%s params=%v", req.Action, req.Params)
		return qqBotAPIError("unsupported qqbot api action: " + req.Action), nil
	}
}

func (d *qqBotDriver) handleGatewayPayload(payload []byte, handler func([]byte, zero.APICaller)) {
	var env struct {
		Op int             `json:"op"`
		S  any             `json:"s"`
		T  string          `json:"t"`
		D  json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		logrus.Debugf("[qqbot] invalid gateway json: %v", err)
		return
	}
	if env.S != nil {
		d.lastSeq = env.S
	}
	switch env.Op {
	case 10:
		var hello struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		}
		_ = json.Unmarshal(env.D, &hello)
		if hello.HeartbeatInterval <= 0 {
			hello.HeartbeatInterval = 30000
		}
		d.identify()
		d.startHeartbeat(time.Duration(hello.HeartbeatInterval) * time.Millisecond)
	case 0:
		switch env.T {
		case "READY":
			logrus.Infof("[qqbot] gateway ready name=%s", d.name)
		case "C2C_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE":
			if event := d.zeroEvent(env.T, env.D); len(event) > 0 {
				handler(event, d)
			}
		}
	case 7:
		logrus.Infof("[qqbot] gateway requested reconnect")
		d.closeConn()
	case 9:
		logrus.Warnf("[qqbot] invalid session")
		d.closeConn()
	}
}

func (d *qqBotDriver) identify() {
	body := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token":   "QQBot " + d.token,
			"intents": qqBotIntents,
			"shard":   []int{0, 1},
		},
	}
	d.writeJSON(body)
}

func (d *qqBotDriver) startHeartbeat(interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	d.mu.Lock()
	if d.heartbeatCancel != nil {
		d.heartbeatCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d.heartbeatCancel = cancel
	d.mu.Unlock()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.writeJSON(map[string]any{"op": 1, "d": d.lastSeq})
			}
		}
	}()
}

func (d *qqBotDriver) zeroEvent(eventType string, raw json.RawMessage) []byte {
	var msg struct {
		ID          string `json:"id"`
		Content     string `json:"content"`
		GroupOpenID string `json:"group_openid"`
		Author      struct {
			UserOpenID   string `json:"user_openid"`
			MemberOpenID string `json:"member_openid"`
		} `json:"author"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		logrus.Debugf("[qqbot] message decode failed: %v", err)
		return nil
	}
	content := strings.TrimSpace(msg.Content)
	if content == "" {
		return nil
	}
	now := time.Now().Unix()
	if msg.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
			now = ts.Unix()
		}
	}
	messageID := qqBotStableID("msg:" + firstNonEmpty(msg.ID, msg.Timestamp, content))
	segments := []message.Segment{message.Text(content)}
	event := map[string]any{
		"time":         now,
		"self_id":      d.selfID,
		"post_type":    "message",
		"message_id":   messageID,
		"raw_message":  content,
		"message":      segments,
		"font":         0,
		"qqbot_source": eventType,
	}
	switch eventType {
	case "C2C_MESSAGE_CREATE":
		userOpenID := msg.Author.UserOpenID
		userID := qqBotStableID("user:" + userOpenID)
		d.rememberTarget(userID, userOpenID, false)
		event["message_type"] = "private"
		event["sub_type"] = "friend"
		event["user_id"] = userID
		event["sender"] = map[string]any{"user_id": userID, "nickname": "QQBot User"}
		logrus.Infof("[qqbot] mapped private message message_id=%d user_id=%d text=%q", messageID, userID, truncate(content, 160))
	case "GROUP_AT_MESSAGE_CREATE":
		memberOpenID := msg.Author.MemberOpenID
		groupID := qqBotStableID("group:" + msg.GroupOpenID)
		userID := qqBotStableID("member:" + memberOpenID)
		d.rememberTarget(groupID, msg.GroupOpenID, true)
		d.rememberTarget(userID, memberOpenID, false)
		event["message_type"] = "group"
		event["sub_type"] = "normal"
		event["group_id"] = groupID
		event["user_id"] = userID
		event["sender"] = map[string]any{"user_id": userID, "nickname": "QQBot Member", "role": "member"}
		logrus.Infof("[qqbot] mapped group message message_id=%d group_id=%d user_id=%d text=%q", messageID, groupID, userID, truncate(content, 160))
	default:
		return nil
	}
	out, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return out
}

func (d *qqBotDriver) sendOfficialMessage(ctx context.Context, openID string, group bool, msg any) (zero.APIResponse, error) {
	content, attachments := d.messageParts(msg)
	if len(attachments) == 0 {
		return d.sendOfficialText(ctx, openID, group, content)
	}
	var last zero.APIResponse
	if strings.TrimSpace(content) != "" {
		resp, err := d.sendOfficialText(ctx, openID, group, content)
		if err != nil {
			return resp, err
		}
		last = resp
	}
	for _, item := range attachments {
		resp, err := d.sendOfficialMedia(ctx, openID, group, item)
		if err != nil {
			fallback := strings.TrimSpace(d.messageText(msg))
			if fallback != "" {
				logrus.Warnf("[qqbot] media_send_failed target=%s error=%v; falling back to markdown text", item.target, err)
				return d.sendOfficialText(ctx, openID, group, fallback)
			}
			return resp, err
		}
		last = resp
	}
	if last.Status == "" {
		return qqBotAPIError("empty message"), nil
	}
	return last, nil
}

func (d *qqBotDriver) sendOfficialText(ctx context.Context, openID string, group bool, content string) (zero.APIResponse, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return qqBotAPIError("empty message"), nil
	}
	token, err := d.accessToken()
	if err != nil {
		return zero.APIResponse{}, err
	}
	path := "/v2/users/" + openID + "/messages"
	if group {
		path = "/v2/groups/" + openID + "/messages"
	}
	useMarkdown := d.useMarkdown || strings.Contains(content, "![")
	body := map[string]any{"content": content, "msg_type": 0}
	if useMarkdown {
		body = map[string]any{"markdown": map[string]any{"content": content}, "msg_type": 2}
	}
	data, err := d.apiRequest(ctx, token, http.MethodPost, path, body)
	if err != nil {
		return zero.APIResponse{}, err
	}
	id := qqBotStableID("sent:" + firstNonEmpty(gjson.GetBytes(data, "id").String(), gjson.GetBytes(data, "message_id").String(), time.Now().String()))
	targetType := "private"
	if group {
		targetType = "group"
	}
	logrus.Infof("[qqbot] sent message target_type=%s message_id=%d markdown=%v content_len=%d", targetType, id, useMarkdown, len(content))
	return qqBotAPIOK(map[string]any{"message_id": id}), nil
}

func (d *qqBotDriver) sendOfficialMedia(ctx context.Context, openID string, group bool, item qqBotMediaAttachment) (zero.APIResponse, error) {
	token, err := d.accessToken()
	if err != nil {
		return zero.APIResponse{}, err
	}
	fileInfo, err := d.uploadOfficialMedia(ctx, token, openID, group, item)
	if err != nil {
		return zero.APIResponse{}, err
	}
	path := "/v2/users/" + openID + "/messages"
	targetType := "private"
	if group {
		path = "/v2/groups/" + openID + "/messages"
		targetType = "group"
	}
	body := map[string]any{
		"msg_type": 7,
		"media": map[string]any{
			"file_info": fileInfo,
		},
	}
	data, err := d.apiRequest(ctx, token, http.MethodPost, path, body)
	if err != nil {
		return zero.APIResponse{}, err
	}
	id := qqBotStableID("media:" + firstNonEmpty(gjson.GetBytes(data, "id").String(), gjson.GetBytes(data, "message_id").String(), time.Now().String()))
	logrus.Infof("[qqbot] sent media target_type=%s message_id=%d file_type=%d target=%s", targetType, id, item.fileType, item.target)
	return qqBotAPIOK(map[string]any{"message_id": id}), nil
}

func (d *qqBotDriver) uploadOfficialMedia(ctx context.Context, token, openID string, group bool, item qqBotMediaAttachment) (string, error) {
	path := "/v2/users/" + openID + "/files"
	if group {
		path = "/v2/groups/" + openID + "/files"
	}
	body := map[string]any{
		"file_type":    item.fileType,
		"srv_send_msg": false,
	}
	if strings.HasPrefix(item.target, "http://") || strings.HasPrefix(item.target, "https://") {
		body["url"] = item.target
	} else {
		data, err := os.ReadFile(item.target)
		if err != nil {
			return "", err
		}
		if len(data) > qqBotMediaMaxUploadSize {
			return "", fmt.Errorf("qqbot media too large: %.2fMB", float64(len(data))/1024/1024)
		}
		body["file_data"] = base64.StdEncoding.EncodeToString(data)
	}
	data, err := d.apiRequest(ctx, token, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	fileInfo := firstNonEmpty(
		gjson.GetBytes(data, "file_info").String(),
		gjson.GetBytes(data, "data.file_info").String(),
	)
	if fileInfo == "" {
		return "", fmt.Errorf("qqbot upload response missing file_info: %s", truncate(string(data), 240))
	}
	return fileInfo, nil
}

func (d *qqBotDriver) accessToken() (string, error) {
	d.mu.Lock()
	if d.token != "" && time.Now().Before(d.tokenExpiresAt.Add(-5*time.Minute)) {
		token := d.token
		d.mu.Unlock()
		return token, nil
	}
	d.mu.Unlock()
	body := map[string]string{"appId": d.appID, "clientSecret": d.appSecret}
	data, err := d.apiRequest(context.Background(), "", http.MethodPost, qqBotTokenURL, body)
	if err != nil {
		return "", err
	}
	token := gjson.GetBytes(data, "access_token").String()
	if token == "" {
		return "", fmt.Errorf("qqbot token missing: %s", string(data))
	}
	expiresIn := gjson.GetBytes(data, "expires_in").Int()
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	d.mu.Lock()
	d.token = token
	d.tokenExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	d.mu.Unlock()
	return token, nil
}

func (d *qqBotDriver) gatewayURL(token string) (string, error) {
	data, err := d.apiRequest(context.Background(), token, http.MethodGet, "/gateway", nil)
	if err != nil {
		return "", err
	}
	u := gjson.GetBytes(data, "url").String()
	if u == "" {
		return "", fmt.Errorf("qqbot gateway url missing: %s", string(data))
	}
	return u, nil
}

func (d *qqBotDriver) apiRequest(ctx context.Context, token, method, path string, body any) ([]byte, error) {
	u := path
	if strings.HasPrefix(path, "/") {
		u = qqBotAPIBase + path
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", qqBotUserAgent)
	if token != "" {
		req.Header.Set("Authorization", "QQBot "+token)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("qqbot api %s %s status=%d body=%s", method, path, resp.StatusCode, truncate(string(data), 240))
	}
	return data, nil
}

func (d *qqBotDriver) writeJSON(v any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return
	}
	if err := d.conn.WriteJSON(v); err != nil {
		logrus.Debugf("[qqbot] write gateway failed: %v", err)
	}
}

func (d *qqBotDriver) closeConn() {
	d.mu.Lock()
	if d.heartbeatCancel != nil {
		d.heartbeatCancel()
		d.heartbeatCancel = nil
	}
	if d.conn != nil {
		_ = d.conn.Close()
		d.conn = nil
	}
	d.mu.Unlock()
}

func (d *qqBotDriver) rememberTarget(id int64, openID string, group bool) {
	if strings.TrimSpace(openID) == "" {
		return
	}
	d.targetMu.Lock()
	d.targets[id] = qqBotTarget{openID: openID, group: group}
	d.targetMu.Unlock()
}

func (d *qqBotDriver) targetFor(id int64, group bool) (qqBotTarget, bool) {
	d.targetMu.RLock()
	target, ok := d.targets[id]
	d.targetMu.RUnlock()
	if ok && target.group == group {
		return target, true
	}
	if group && d.defaultGroupID != "" {
		return qqBotTarget{openID: d.defaultGroupID, group: true}, true
	}
	if !group && d.defaultOpenID != "" {
		return qqBotTarget{openID: d.defaultOpenID, group: false}, true
	}
	return qqBotTarget{}, false
}

func (d *qqBotDriver) messageText(v any) string {
	switch msg := v.(type) {
	case string:
		return qqBotCleanCQText(msg)
	case message.Message:
		return d.segmentsText(msg)
	case []message.Segment:
		return d.segmentsText(message.Message(msg))
	case []any:
		segments := make(message.Message, 0, len(msg))
		for _, item := range msg {
			if seg, ok := item.(message.Segment); ok {
				segments = append(segments, seg)
			}
		}
		if len(segments) > 0 {
			return d.segmentsText(segments)
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func (d *qqBotDriver) messageParts(v any) (string, []qqBotMediaAttachment) {
	switch msg := v.(type) {
	case message.Message:
		return d.segmentsParts(msg)
	case []message.Segment:
		return d.segmentsParts(message.Message(msg))
	case []any:
		segments := make(message.Message, 0, len(msg))
		for _, item := range msg {
			if seg, ok := item.(message.Segment); ok {
				segments = append(segments, seg)
			}
		}
		if len(segments) > 0 {
			return d.segmentsParts(segments)
		}
	case string:
		content, items := d.mediaLineParts(msg)
		if len(items) > 0 {
			return qqBotCleanCQText(content), items
		}
	}
	return d.messageText(v), nil
}

func (d *qqBotDriver) segmentsParts(msg message.Message) (string, []qqBotMediaAttachment) {
	var b strings.Builder
	items := []qqBotMediaAttachment{}
	for _, seg := range msg {
		switch seg.Type {
		case "text":
			text, mediaItems := d.mediaLineParts(seg.Data["text"])
			b.WriteString(text)
			items = append(items, mediaItems...)
		case "image":
			file := strings.TrimSpace(seg.Data["file"])
			if item, ok := d.mediaAttachment(file, qqBotMediaTypeImage); ok {
				items = append(items, item)
			} else if markdown, ok := d.imageMarkdown(file); ok {
				b.WriteString(markdown)
			} else {
				b.WriteString("\n[图片]\n")
			}
		case "video":
			file := strings.TrimSpace(firstNonEmpty(seg.Data["file"], seg.Data["url"]))
			if item, ok := d.mediaAttachment(file, qqBotMediaTypeVideo); ok {
				items = append(items, item)
			} else if file != "" {
				b.WriteString("\n")
				b.WriteString(file)
				b.WriteString("\n")
			}
		case "at":
			b.WriteString("@")
			b.WriteString(seg.Data["qq"])
			b.WriteString(" ")
		case "node":
			if name := strings.TrimSpace(seg.Data["name"]); name != "" {
				b.WriteString(name)
				b.WriteString(": ")
			}
			b.WriteString(qqBotCleanCQText(seg.Data["content"]))
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String()), items
}

func (d *qqBotDriver) mediaLineParts(text string) (string, []qqBotMediaAttachment) {
	lines := strings.Split(text, "\n")
	items := []qqBotMediaAttachment{}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		raw := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(raw), "MEDIA:") {
			kept = append(kept, line)
			continue
		}
		target := strings.TrimSpace(raw[len("MEDIA:"):])
		item, ok := d.mediaAttachment(target, qqBotMediaTypeImage)
		if !ok {
			kept = append(kept, line)
			continue
		}
		items = append(items, item)
	}
	return strings.Join(kept, "\n"), items
}

func (d *qqBotDriver) mediaAttachment(file string, fallbackType int) (qqBotMediaAttachment, bool) {
	file = strings.TrimSpace(file)
	if file == "" {
		return qqBotMediaAttachment{}, false
	}
	if strings.HasPrefix(file, "http://") || strings.HasPrefix(file, "https://") {
		return qqBotMediaAttachment{fileType: qqBotMediaTypeByName(file, fallbackType), target: file}, true
	}
	localPath := qqBotLocalMediaPath(file)
	if localPath == "" || !qqBotAllowedLocalMediaPath(localPath) {
		return qqBotMediaAttachment{}, false
	}
	if st, err := os.Stat(localPath); err != nil || st.IsDir() {
		return qqBotMediaAttachment{}, false
	}
	return qqBotMediaAttachment{fileType: qqBotMediaTypeByName(localPath, fallbackType), target: localPath}, true
}

func qqBotLocalMediaPath(file string) string {
	file = strings.TrimSpace(file)
	if strings.HasPrefix(file, "file://") {
		u, err := url.Parse(file)
		if err != nil {
			return ""
		}
		file = u.Path
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return ""
	}
	return abs
}

func qqBotAllowedLocalMediaPath(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	allowed := []string{cacheDir, os.TempDir()}
	for _, root := range allowed {
		rootAbs, err := filepath.Abs(root)
		if err != nil || rootAbs == "" {
			continue
		}
		rel, err := filepath.Rel(rootAbs, abs)
		if err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}

func qqBotMediaTypeByName(name string, fallback int) int {
	ext := strings.ToLower(filepath.Ext(strings.Split(name, "?")[0]))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return qqBotMediaTypeImage
	case ".mp4", ".mov", ".m4v":
		return qqBotMediaTypeVideo
	default:
		return fallback
	}
}

func (d *qqBotDriver) segmentsText(msg message.Message) string {
	var b strings.Builder
	for _, seg := range msg {
		switch seg.Type {
		case "text":
			b.WriteString(seg.Data["text"])
		case "image":
			file := strings.TrimSpace(seg.Data["file"])
			if markdown, ok := d.imageMarkdown(file); ok {
				b.WriteString(markdown)
			} else {
				b.WriteString("\n[图片]\n")
			}
		case "at":
			b.WriteString("@")
			b.WriteString(seg.Data["qq"])
			b.WriteString(" ")
		case "node":
			if name := strings.TrimSpace(seg.Data["name"]); name != "" {
				b.WriteString(name)
				b.WriteString(": ")
			}
			b.WriteString(qqBotCleanCQText(seg.Data["content"]))
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func (d *qqBotDriver) forwardMessageText(v any) string {
	text := d.messageText(v)
	if text == "" {
		return ""
	}
	return "官方 QQBot 暂不支持 OneBot 合并转发，已降级为文本：\n\n" + text
}

func (d *qqBotDriver) imageMarkdown(file string) (string, bool) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", false
	}
	if strings.HasPrefix(file, "http://") || strings.HasPrefix(file, "https://") {
		return "\n" + sizedQQMarkdownImage(file, 0, 0) + "\n", true
	}
	publicURL, localPath, ok := d.publicImageURL(file)
	if !ok {
		return "", false
	}
	w, h := localImageSize(localPath)
	return "\n" + sizedQQMarkdownImage(publicURL, w, h) + "\n", true
}

func (d *qqBotDriver) publicImageURL(file string) (string, string, bool) {
	if strings.TrimSpace(d.publicBaseURL) == "" {
		return "", "", false
	}
	localPath := strings.TrimSpace(file)
	if strings.HasPrefix(localPath, "file://") {
		u, err := url.Parse(localPath)
		if err != nil {
			return "", "", false
		}
		localPath = u.Path
	}
	localAbs, err := filepath.Abs(localPath)
	if err != nil {
		return "", "", false
	}
	cacheAbs, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", "", false
	}
	rel, err := filepath.Rel(cacheAbs, localAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", "", false
	}
	publicURL, err := url.JoinPath(d.publicBaseURL, filepath.ToSlash(rel))
	if err != nil {
		return "", "", false
	}
	return publicURL, localAbs, true
}

func localImageSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return fitQQMarkdownImageSize(cfg.Width, cfg.Height)
}

func fitQQMarkdownImageSize(width, height int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	maxW, maxH := 900, 1200
	if width <= maxW && height <= maxH {
		return width, height
	}
	rw := float64(maxW) / float64(width)
	rh := float64(maxH) / float64(height)
	r := rw
	if rh < r {
		r = rh
	}
	return max(1, int(float64(width)*r)), max(1, int(float64(height)*r))
}

func sizedQQMarkdownImage(imageURL string, width, height int) string {
	if width > 0 && height > 0 {
		return fmt.Sprintf("![image #%dpx #%dpx](%s)", width, height, imageURL)
	}
	return fmt.Sprintf("![image](%s)", imageURL)
}

func qqBotCleanCQText(s string) string {
	s = qqBotCQCodePattern.ReplaceAllStringFunc(s, func(code string) string {
		match := qqBotCQCodePattern.FindStringSubmatch(code)
		if len(match) < 2 {
			return ""
		}
		typ := match[1]
		switch typ {
		case "image":
			if u := cqAttr(code, "url"); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				return "\n![image](" + u + ")\n"
			}
			return "\n[图片]\n"
		case "video":
			return "\n[视频]\n"
		case "at":
			if qq := cqAttr(code, "qq"); qq != "" {
				return "@" + qq + " "
			}
			return ""
		case "reply":
			return ""
		default:
			return ""
		}
	})
	return strings.TrimSpace(s)
}

func cqAttr(code, key string) string {
	prefix := key + "="
	for _, part := range strings.Split(strings.Trim(code, "[]"), ",") {
		if strings.HasPrefix(part, prefix) {
			return strings.TrimPrefix(part, prefix)
		}
	}
	return ""
}

func int64Param(params zero.Params, key string) int64 {
	v, ok := params[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		i, _ := strconv.ParseInt(fmt.Sprint(v), 10, 64)
		return i
	}
}

func qqBotStableID(s string) int64 {
	if strings.TrimSpace(s) == "" {
		s = time.Now().String()
	}
	table := crc64.MakeTable(crc64.ISO)
	sum := crc64.Checksum([]byte(s), table) & 0x7fffffffffffffff
	if sum <= 0xffffffff {
		h := sha1.Sum([]byte(s))
		n, _ := strconv.ParseInt(hex.EncodeToString(h[:8])[:15], 16, 64)
		sum = uint64(n & 0x7fffffffffffffff)
	}
	return int64(sum)
}

func qqBotAPIOK(data map[string]any) zero.APIResponse {
	raw, _ := json.Marshal(data)
	return zero.APIResponse{Status: "ok", RetCode: 0, Data: gjson.ParseBytes(raw)}
}

func qqBotAPIError(msg string) zero.APIResponse {
	return zero.APIResponse{Status: "failed", RetCode: 1, Message: msg, Wording: msg}
}
