package mediaparser

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
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
)

type qqBotDriver struct {
	appID          string
	appSecret      string
	defaultOpenID  string
	defaultGroupID string
	name           string
	useMarkdown    bool

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
	case "get_login_info":
		return qqBotAPIOK(map[string]any{"user_id": d.selfID, "nickname": d.name}), nil
	default:
		logrus.Debugf("[qqbot] unsupported api action=%s", req.Action)
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
	content := strings.TrimSpace(qqBotMessageText(msg))
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
	body := map[string]any{"content": content, "msg_type": 0}
	if d.useMarkdown {
		body = map[string]any{"markdown": map[string]any{"content": content}, "msg_type": 2}
	}
	data, err := d.apiRequest(ctx, token, http.MethodPost, path, body)
	if err != nil {
		return zero.APIResponse{}, err
	}
	id := qqBotStableID("sent:" + firstNonEmpty(gjson.GetBytes(data, "id").String(), gjson.GetBytes(data, "message_id").String(), time.Now().String()))
	return qqBotAPIOK(map[string]any{"message_id": id}), nil
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

func qqBotMessageText(v any) string {
	switch msg := v.(type) {
	case string:
		return msg
	case message.Message:
		return qqBotSegmentsText(msg)
	case []message.Segment:
		return qqBotSegmentsText(message.Message(msg))
	case []any:
		segments := make(message.Message, 0, len(msg))
		for _, item := range msg {
			if seg, ok := item.(message.Segment); ok {
				segments = append(segments, seg)
			}
		}
		if len(segments) > 0 {
			return qqBotSegmentsText(segments)
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func qqBotSegmentsText(msg message.Message) string {
	var b strings.Builder
	for _, seg := range msg {
		switch seg.Type {
		case "text":
			b.WriteString(seg.Data["text"])
		case "image":
			file := strings.TrimSpace(seg.Data["file"])
			if strings.HasPrefix(file, "http://") || strings.HasPrefix(file, "https://") {
				fmt.Fprintf(&b, "\n![image](%s)\n", file)
			} else {
				b.WriteString("\n[图片]\n")
			}
		case "at":
			b.WriteString("@")
			b.WriteString(seg.Data["qq"])
			b.WriteString(" ")
		}
	}
	return strings.TrimSpace(b.String())
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
