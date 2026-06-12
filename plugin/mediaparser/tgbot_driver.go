package mediaparser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/message"
)

const (
	tgBotDefaultAPIBase = "https://api.telegram.org"
	tgBotUserAgent      = "ZeroBot-Plugin/TGBot"

	tgBotMediaPhoto    = "photo"
	tgBotMediaVideo    = "video"
	tgBotMediaDocument = "document"
)

type tgBotDriver struct {
	token        string
	apiBase      string
	name         string
	mediaEnabled bool

	client *http.Client
	selfID int64

	targetMu sync.RWMutex
	targets  map[int64]tgBotTarget
}

type tgBotTarget struct {
	chatID int64
	group  bool
}

type tgBotUpdate struct {
	UpdateID    int64        `json:"update_id"`
	Message     tgBotMessage `json:"message"`
	ChannelPost tgBotMessage `json:"channel_post"`
}

type tgBotMessage struct {
	MessageID int64     `json:"message_id"`
	Date      int64     `json:"date"`
	Text      string    `json:"text"`
	Caption   string    `json:"caption"`
	Chat      tgBotChat `json:"chat"`
	From      tgBotUser `json:"from"`
	Sender    tgBotUser `json:"sender_chat"`
}

type tgBotChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type tgBotUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type tgBotMediaAttachment struct {
	kind   string
	target string
	name   string
}

// NewTelegramBotDriver creates the optional Telegram Bot driver from system settings.
func NewTelegramBotDriver(settings SystemSettings) (zero.Driver, bool) {
	settings = normalizeSystemSettings(settings)
	if !settings.TGBotEnabled || settings.TGBotToken == "" {
		return nil, false
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if proxy := strings.TrimSpace(settings.TGBotProxy); proxy != "" {
		if u, err := url.Parse(proxy); err == nil {
			transport.Proxy = http.ProxyURL(u)
		} else {
			logrus.Warnf("[tgbot] invalid proxy ignored: %v", err)
		}
	}
	apiBase := firstNonEmpty(settings.TGBotAPIBase, tgBotDefaultAPIBase)
	return &tgBotDriver{
		token:        settings.TGBotToken,
		apiBase:      strings.TrimRight(apiBase, "/"),
		name:         firstNonEmpty(settings.TGBotName, "telegram"),
		mediaEnabled: settings.TGBotMediaEnabled,
		client:       &http.Client{Timeout: 90 * time.Second, Transport: transport},
		selfID:       tgBotStableID("self:" + settings.TGBotToken),
		targets:      map[int64]tgBotTarget{},
	}, true
}

func (d *tgBotDriver) Connect() {
	zero.APICallers.Store(d.selfID, d)
	logrus.Infof("[tgbot] long polling enabled name=%s self=%d api_base=%s media=%v", d.name, d.selfID, d.apiBase, d.mediaEnabled)
}

func (d *tgBotDriver) Listen(handler func([]byte, zero.APICaller)) {
	d.Connect()
	offset := int64(0)
	for {
		updates, err := d.getUpdates(offset)
		if err != nil {
			logrus.Warnf("[tgbot] getUpdates failed: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if event := d.zeroEvent(update); len(event) > 0 {
				handler(event, d)
			}
		}
	}
}

func (d *tgBotDriver) CallAPI(ctx context.Context, req zero.APIRequest) (zero.APIResponse, error) {
	switch req.Action {
	case "send_private_msg":
		userID := int64Param(req.Params, "user_id")
		target, ok := d.targetFor(userID, false)
		if !ok {
			return tgBotAPIError("telegram private target not known"), nil
		}
		return d.sendTelegramMessage(ctx, target.chatID, req.Params["message"])
	case "send_group_msg":
		groupID := int64Param(req.Params, "group_id")
		target, ok := d.targetFor(groupID, true)
		if !ok {
			return tgBotAPIError("telegram group target not known"), nil
		}
		return d.sendTelegramMessage(ctx, target.chatID, req.Params["message"])
	case "send_private_forward_msg":
		userID := int64Param(req.Params, "user_id")
		target, ok := d.targetFor(userID, false)
		if !ok {
			return tgBotAPIError("telegram private target not known"), nil
		}
		return d.sendTelegramText(ctx, target.chatID, d.forwardMessageText(req.Params["messages"]))
	case "send_group_forward_msg":
		groupID := int64Param(req.Params, "group_id")
		target, ok := d.targetFor(groupID, true)
		if !ok {
			return tgBotAPIError("telegram group target not known"), nil
		}
		return d.sendTelegramText(ctx, target.chatID, d.forwardMessageText(req.Params["messages"]))
	case "get_login_info":
		return tgBotAPIOK(map[string]any{"user_id": d.selfID, "nickname": d.name}), nil
	case "mark_msg_as_read":
		return tgBotAPIOK(map[string]any{"message_id": int64Param(req.Params, "message_id")}), nil
	default:
		logrus.Infof("[tgbot] unsupported api action=%s params=%v", req.Action, req.Params)
		return tgBotAPIError("unsupported telegram api action: " + req.Action), nil
	}
}

func (d *tgBotDriver) getUpdates(offset int64) ([]tgBotUpdate, error) {
	body := map[string]any{
		"timeout":         50,
		"allowed_updates": []string{"message", "channel_post"},
	}
	if offset > 0 {
		body["offset"] = offset
	}
	data, err := d.apiJSON(context.Background(), "getUpdates", body)
	if err != nil {
		return nil, err
	}
	if !data.Get("ok").Bool() {
		return nil, fmt.Errorf("telegram getUpdates not ok: %s", truncate(data.Raw, 240))
	}
	var updates []tgBotUpdate
	if err := json.Unmarshal([]byte(data.Get("result").Raw), &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (d *tgBotDriver) zeroEvent(update tgBotUpdate) []byte {
	msg := update.Message
	eventType := "message"
	if msg.MessageID == 0 && update.ChannelPost.MessageID != 0 {
		msg = update.ChannelPost
		eventType = "channel_post"
	}
	if msg.MessageID == 0 || msg.Chat.ID == 0 {
		return nil
	}
	content := firstNonEmpty(msg.Text, msg.Caption)
	if strings.TrimSpace(content) == "" {
		return nil
	}
	group := tgBotIsGroupChat(msg.Chat.Type)
	targetID := tgBotStableID("chat:" + strconv.FormatInt(msg.Chat.ID, 10))
	userID := tgBotStableID("user:" + strconv.FormatInt(firstNonZeroInt64(msg.From.ID, msg.Sender.ID, msg.Chat.ID), 10))
	if ok, reason := tgBotAccessOK(tgBotRuntimeSettings(), group, userID, targetID); !ok {
		if group {
			logrus.Infof("[tgbot] access_blocked type=group reason=%s message_id=%d group_id=%d user_id=%d chat_id=%d", reason, msg.MessageID, targetID, userID, msg.Chat.ID)
		} else {
			logrus.Infof("[tgbot] access_blocked type=private reason=%s message_id=%d user_id=%d chat_id=%d", reason, msg.MessageID, userID, msg.Chat.ID)
		}
		return nil
	}
	d.rememberTarget(targetID, msg.Chat.ID, group)
	if !group {
		d.rememberTarget(userID, msg.Chat.ID, false)
	}
	event := map[string]any{
		"time":                firstNonZeroInt64(msg.Date, time.Now().Unix()),
		"self_id":             d.selfID,
		"post_type":           "message",
		"message_id":          tgBotStableID(fmt.Sprintf("msg:%d:%d", msg.Chat.ID, msg.MessageID)),
		"message":             content,
		"raw_message":         content,
		"font":                0,
		"tgbot_source":        eventType,
		"telegram_chat_id":    msg.Chat.ID,
		"telegram_message_id": msg.MessageID,
	}
	if group {
		event["message_type"] = "group"
		event["sub_type"] = "normal"
		event["group_id"] = targetID
		event["user_id"] = userID
		event["sender"] = map[string]any{"user_id": userID, "nickname": tgBotSenderName(msg), "role": "member"}
		logrus.Infof("[tgbot] mapped group message message_id=%d group_id=%d user_id=%d text=%q", msg.MessageID, targetID, userID, truncate(content, 160))
	} else {
		event["message_type"] = "private"
		event["sub_type"] = "friend"
		event["user_id"] = userID
		event["sender"] = map[string]any{"user_id": userID, "nickname": tgBotSenderName(msg)}
		logrus.Infof("[tgbot] mapped private message message_id=%d user_id=%d text=%q", msg.MessageID, userID, truncate(content, 160))
	}
	out, err := json.Marshal(event)
	if err != nil {
		return nil
	}
	return out
}

func tgBotRuntimeSettings() SystemSettings {
	systemMu.RLock()
	settings := runtimeSystem
	systemMu.RUnlock()
	return normalizeSystemSettings(settings)
}

func tgBotAccessOK(settings SystemSettings, group bool, userID, groupID int64) (bool, string) {
	settings = normalizeSystemSettings(settings)
	if !group {
		return tgBotAccessListOK(settings.TGBotPrivateMode, userID, settings.TGBotUserWhitelist, settings.TGBotUserBlacklist, "private")
	}
	if ok, reason := tgBotAccessListOK(settings.TGBotGroupMode, groupID, settings.TGBotGroupWhitelist, settings.TGBotGroupBlacklist, "group"); !ok {
		return false, reason
	}
	return tgBotAccessListOK(settings.TGBotGroupUserMode, userID, settings.TGBotGroupUserWhitelist, settings.TGBotGroupUserBlacklist, "group_user")
}

func tgBotAccessListOK(mode string, id int64, whitelist, blacklist []int64, scope string) (bool, string) {
	switch normalizeSystemAccessMode(mode) {
	case accessWhitelist:
		if int64SliceContains(whitelist, id) {
			return true, scope + "_whitelisted"
		}
		return false, scope + "_not_in_whitelist"
	case accessBlacklist:
		if int64SliceContains(blacklist, id) {
			return false, scope + "_blacklisted"
		}
		return true, scope + "_not_blacklisted"
	default:
		return true, scope + "_access_none"
	}
}

func int64SliceContains(list []int64, id int64) bool {
	for _, item := range list {
		if item == id {
			return true
		}
	}
	return false
}

func (d *tgBotDriver) sendTelegramMessage(ctx context.Context, chatID int64, msg any) (zero.APIResponse, error) {
	content, attachments := d.messageParts(msg)
	var last zero.APIResponse
	if strings.TrimSpace(content) != "" {
		resp, err := d.sendTelegramText(ctx, chatID, content)
		if err != nil {
			return resp, err
		}
		last = resp
	}
	if d.mediaEnabled {
		for _, item := range attachments {
			resp, err := d.sendTelegramMedia(ctx, chatID, item)
			if err != nil {
				logrus.Warnf("[tgbot] media_send_failed kind=%s target=%s error=%v", item.kind, item.target, err)
				return resp, err
			}
			last = resp
		}
	}
	if last.Status == "" {
		return tgBotAPIError("empty message"), nil
	}
	return last, nil
}

func (d *tgBotDriver) sendTelegramText(ctx context.Context, chatID int64, content string) (zero.APIResponse, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return tgBotAPIError("empty message"), nil
	}
	data, err := d.apiJSON(ctx, "sendMessage", map[string]any{
		"chat_id": chatID,
		"text":    content,
	})
	if err != nil {
		return zero.APIResponse{}, err
	}
	id := data.Get("result.message_id").Int()
	logrus.Infof("[tgbot] sent message chat_id=%d message_id=%d content_len=%d", chatID, id, len(content))
	return tgBotAPIOK(map[string]any{"message_id": id}), nil
}

func (d *tgBotDriver) sendTelegramMedia(ctx context.Context, chatID int64, item tgBotMediaAttachment) (zero.APIResponse, error) {
	method, field := tgBotSendMethod(item.kind)
	if method == "" {
		return tgBotAPIError("unsupported telegram media kind: " + item.kind), nil
	}
	fields := map[string]string{"chat_id": strconv.FormatInt(chatID, 10)}
	data, err := d.apiMultipart(ctx, method, fields, field, item.target, item.name)
	if err != nil {
		return zero.APIResponse{}, err
	}
	id := data.Get("result.message_id").Int()
	logrus.Infof("[tgbot] sent media chat_id=%d message_id=%d kind=%s target=%s", chatID, id, item.kind, item.target)
	return tgBotAPIOK(map[string]any{"message_id": id}), nil
}

func (d *tgBotDriver) messageParts(v any) (string, []tgBotMediaAttachment) {
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
		content, cqItems := d.cqMediaParts(content)
		items = append(items, cqItems...)
		if len(items) > 0 {
			return qqBotCleanCQText(content), items
		}
		return qqBotCleanCQText(msg), nil
	}
	b, _ := json.Marshal(v)
	return string(b), nil
}

func (d *tgBotDriver) segmentsParts(msg message.Message) (string, []tgBotMediaAttachment) {
	var b strings.Builder
	items := []tgBotMediaAttachment{}
	for _, seg := range msg {
		switch seg.Type {
		case "text":
			b.WriteString(seg.Data["text"])
		case "image":
			file := strings.TrimSpace(seg.Data["file"])
			if item, ok := d.mediaAttachment(file, tgBotMediaPhoto); ok {
				items = append(items, item)
			} else {
				b.WriteString("\n[图片]\n")
			}
		case "video":
			file := strings.TrimSpace(firstNonEmpty(seg.Data["file"], seg.Data["url"]))
			if item, ok := d.mediaAttachment(file, tgBotMediaVideo); ok {
				items = append(items, item)
			} else if file != "" {
				b.WriteString("\n")
				b.WriteString(file)
				b.WriteString("\n")
			}
		case "file":
			file := strings.TrimSpace(seg.Data["file"])
			if item, ok := d.mediaAttachment(file, tgBotMediaDocument); ok {
				if name := strings.TrimSpace(seg.Data["name"]); name != "" {
					item.name = name
				}
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

func (d *tgBotDriver) mediaLineParts(text string) (string, []tgBotMediaAttachment) {
	lines := strings.Split(text, "\n")
	items := []tgBotMediaAttachment{}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		raw := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToUpper(raw), "MEDIA:") {
			kept = append(kept, line)
			continue
		}
		target := strings.TrimSpace(raw[len("MEDIA:"):])
		item, ok := d.mediaAttachment(target, tgBotMediaPhoto)
		if !ok {
			kept = append(kept, line)
			continue
		}
		items = append(items, item)
	}
	return strings.Join(kept, "\n"), items
}

func (d *tgBotDriver) cqMediaParts(text string) (string, []tgBotMediaAttachment) {
	items := []tgBotMediaAttachment{}
	kept := qqBotCQCodePattern.ReplaceAllStringFunc(text, func(code string) string {
		match := qqBotCQCodePattern.FindStringSubmatch(code)
		if len(match) < 2 {
			return code
		}
		switch strings.ToLower(match[1]) {
		case "image":
			target := firstNonEmpty(cqAttr(code, "file"), cqAttr(code, "url"))
			if item, ok := d.mediaAttachment(target, tgBotMediaPhoto); ok {
				items = append(items, item)
				return ""
			}
		case "video":
			target := firstNonEmpty(cqAttr(code, "file"), cqAttr(code, "url"))
			if item, ok := d.mediaAttachment(target, tgBotMediaVideo); ok {
				items = append(items, item)
				return ""
			}
		}
		return code
	})
	return kept, items
}

func (d *tgBotDriver) mediaAttachment(file, fallbackKind string) (tgBotMediaAttachment, bool) {
	file = strings.TrimSpace(file)
	if file == "" {
		return tgBotMediaAttachment{}, false
	}
	target := stripMediaPrefix(file)
	if strings.HasPrefix(target, "file://") {
		u, err := url.Parse(target)
		if err != nil {
			return tgBotMediaAttachment{}, false
		}
		target = u.Path
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return tgBotMediaAttachment{kind: tgBotMediaKindByName(target, fallbackKind), target: target, name: filepath.Base(strings.Split(target, "?")[0])}, true
	}
	abs, err := filepath.Abs(target)
	if err != nil || !tgBotAllowedLocalMediaPath(abs) {
		return tgBotMediaAttachment{}, false
	}
	if st, err := os.Stat(abs); err != nil || st.IsDir() {
		return tgBotMediaAttachment{}, false
	}
	return tgBotMediaAttachment{kind: tgBotMediaKindByName(abs, fallbackKind), target: abs, name: filepath.Base(abs)}, true
}

func (d *tgBotDriver) forwardMessageText(v any) string {
	text, _ := d.messageParts(v)
	if text == "" {
		return ""
	}
	return "Telegram 暂不支持 OneBot 合并转发，已降级为文本：\n\n" + text
}

func (d *tgBotDriver) apiJSON(ctx context.Context, method string, body any) (tgBotAPIResult, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return tgBotAPIResult{}, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint(method), reader)
	if err != nil {
		return tgBotAPIResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", tgBotUserAgent)
	resp, err := d.client.Do(req)
	if err != nil {
		return tgBotAPIResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return tgBotAPIResult{}, err
	}
	if resp.StatusCode >= 400 {
		return tgBotAPIResult{}, fmt.Errorf("telegram api %s status=%d body=%s", method, resp.StatusCode, truncate(string(data), 240))
	}
	return tgBotAPIResult{Raw: string(data)}, nil
}

func (d *tgBotDriver) apiMultipart(ctx context.Context, method string, fields map[string]string, field, target, name string) (tgBotAPIResult, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			return tgBotAPIResult{}, err
		}
	}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		if err := writer.WriteField(field, target); err != nil {
			return tgBotAPIResult{}, err
		}
	} else {
		f, err := os.Open(target)
		if err != nil {
			return tgBotAPIResult{}, err
		}
		defer f.Close()
		part, err := writer.CreateFormFile(field, firstNonEmpty(name, filepath.Base(target)))
		if err != nil {
			return tgBotAPIResult{}, err
		}
		if _, err := io.Copy(part, f); err != nil {
			return tgBotAPIResult{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return tgBotAPIResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.endpoint(method), &body)
	if err != nil {
		return tgBotAPIResult{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("User-Agent", tgBotUserAgent)
	resp, err := d.client.Do(req)
	if err != nil {
		return tgBotAPIResult{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return tgBotAPIResult{}, err
	}
	if resp.StatusCode >= 400 {
		return tgBotAPIResult{}, fmt.Errorf("telegram api %s status=%d body=%s", method, resp.StatusCode, truncate(string(data), 240))
	}
	return tgBotAPIResult{Raw: string(data)}, nil
}

func (d *tgBotDriver) endpoint(method string) string {
	return d.apiBase + "/bot" + d.token + "/" + method
}

func (d *tgBotDriver) rememberTarget(id, chatID int64, group bool) {
	if id == 0 || chatID == 0 {
		return
	}
	d.targetMu.Lock()
	d.targets[id] = tgBotTarget{chatID: chatID, group: group}
	d.targetMu.Unlock()
}

func (d *tgBotDriver) targetFor(id int64, group bool) (tgBotTarget, bool) {
	d.targetMu.RLock()
	target, ok := d.targets[id]
	d.targetMu.RUnlock()
	if ok && target.group == group {
		return target, true
	}
	return tgBotTarget{}, false
}

type tgBotAPIResult struct {
	Raw string
}

func (r tgBotAPIResult) Get(path string) tgBotAPIResult {
	var v any
	if err := json.Unmarshal([]byte(r.Raw), &v); err != nil {
		return tgBotAPIResult{}
	}
	for _, part := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return tgBotAPIResult{}
		}
		v = m[part]
	}
	b, _ := json.Marshal(v)
	return tgBotAPIResult{Raw: string(b)}
}

func (r tgBotAPIResult) Bool() bool {
	var b bool
	_ = json.Unmarshal([]byte(r.Raw), &b)
	return b
}

func (r tgBotAPIResult) Int() int64 {
	var n float64
	_ = json.Unmarshal([]byte(r.Raw), &n)
	return int64(n)
}

func tgBotIsGroupChat(chatType string) bool {
	switch strings.ToLower(strings.TrimSpace(chatType)) {
	case "group", "supergroup", "channel":
		return true
	default:
		return false
	}
}

func tgBotSenderName(msg tgBotMessage) string {
	user := msg.From
	if user.ID == 0 {
		user = msg.Sender
	}
	name := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	if name != "" {
		return name
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	if msg.Chat.Title != "" {
		return msg.Chat.Title
	}
	if msg.Chat.Username != "" {
		return "@" + msg.Chat.Username
	}
	return "Telegram User"
}

func tgBotAllowedLocalMediaPath(path string) bool {
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

func tgBotMediaKindByName(name, fallback string) string {
	ext := strings.ToLower(filepath.Ext(strings.Split(name, "?")[0]))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return tgBotMediaPhoto
	case ".mp4", ".mov", ".m4v", ".webm":
		return tgBotMediaVideo
	default:
		return firstNonEmpty(fallback, tgBotMediaDocument)
	}
}

func tgBotSendMethod(kind string) (method, field string) {
	switch kind {
	case tgBotMediaPhoto:
		return "sendPhoto", "photo"
	case tgBotMediaVideo:
		return "sendVideo", "video"
	case tgBotMediaDocument:
		return "sendDocument", "document"
	default:
		return "", ""
	}
}

func tgBotStableID(s string) int64 {
	table := crc64.MakeTable(crc64.ISO)
	sum := crc64.Checksum([]byte(s), table)
	return int64(sum & 0x7fffffffffffffff)
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func tgBotAPIOK(data map[string]any) zero.APIResponse {
	return qqBotAPIOK(data)
}

func tgBotAPIError(msg string) zero.APIResponse {
	return qqBotAPIError(msg)
}
