package mediaparser

import (
	stdhtml "html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

type linuxdoContentBlock struct {
	Kind  string
	Text  string
	URL   string
	Level int
}

var linuxdoAttachmentSizePrefixPattern = regexp.MustCompile(`(?i)^(?:\(|（)\s*\d+(?:\.\d+)?\s*(?:B|KB|MB|GB|TB|KIB|MIB|GIB|TIB)\s*(?:\)|）)`)

type linuxdoContentParser struct {
	base       string
	blocks     []linuxdoContentBlock
	seenImages map[string]bool
}

func linuxdoBuildContentBlocks(cooked, base string) []linuxdoContentBlock {
	cooked = strings.TrimSpace(cooked)
	if cooked == "" {
		return nil
	}
	doc, err := xhtml.Parse(strings.NewReader(cooked))
	if err != nil {
		return nil
	}
	root := linuxdoFindHTMLNode(doc, "body")
	if root == nil {
		root = doc
	}
	parser := linuxdoContentParser{
		base:       firstNonEmpty(strings.TrimSpace(base), linuxdoReferer),
		seenImages: map[string]bool{},
	}
	parser.walkChildren(root)
	return parser.blocks
}

func linuxdoHTMLAuthor(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	doc, err := xhtml.Parse(strings.NewReader(body))
	if err != nil {
		return ""
	}
	root := linuxdoFindHTMLNode(doc, "body")
	if root == nil {
		root = doc
	}
	post := linuxdoFindFirstPostNode(root)
	if post == nil {
		return ""
	}
	var author string
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node == nil || author != "" {
			return
		}
		if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, "a") {
			href := linuxdoNodeAttr(node, "href")
			if linuxdoUserProfileURL(href) {
				author = linuxdoCleanBlockText(linuxdoNodeText(node))
				if author == "" {
					author = linuxdoUserProfileName(href)
				}
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(post)
	return author
}

func linuxdoFindFirstPostNode(node *xhtml.Node) *xhtml.Node {
	if node == nil {
		return nil
	}
	if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, "article") {
		if linuxdoNodeAttr(node, "data-post-id") != "" || strings.HasPrefix(strings.ToLower(linuxdoNodeAttr(node, "id")), "post_") || linuxdoNodeHasClass(node, "onscreen-post") || linuxdoNodeHasClass(node, "topic-post") {
			return node
		}
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if post := linuxdoFindFirstPostNode(child); post != nil {
			return post
		}
	}
	return nil
}

func linuxdoUserProfileURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 2 && strings.EqualFold(parts[0], "u") && parts[1] != ""
}

func linuxdoUserProfileName(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "u") {
		return ""
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil {
		return parts[1]
	}
	return strings.TrimSpace(name)
}

func linuxdoFindHTMLNode(node *xhtml.Node, name string) *xhtml.Node {
	if node == nil {
		return nil
	}
	if node.Type == xhtml.ElementNode && strings.EqualFold(node.Data, name) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if found := linuxdoFindHTMLNode(child, name); found != nil {
			return found
		}
	}
	return nil
}

func (parser *linuxdoContentParser) walkChildren(parent *xhtml.Node) {
	for node := parent.FirstChild; node != nil; node = node.NextSibling {
		parser.walkBlock(node)
	}
}

func (parser *linuxdoContentParser) walkBlock(node *xhtml.Node) {
	if node == nil {
		return
	}
	if node.Type == xhtml.TextNode {
		parser.addText("text", node.Data, "")
		return
	}
	if node.Type != xhtml.ElementNode {
		return
	}
	if linuxdoNodeShouldSkip(node) {
		return
	}
	tag := strings.ToLower(node.Data)
	switch tag {
	case "script", "style", "noscript", "svg":
		return
	case "hr":
		parser.addDivider()
	case "img":
		parser.addImage(node)
	case "p":
		parser.walkRich(node, "text", 0)
	case "h1", "h2", "h3", "h4", "h5", "h6", "summary":
		parser.walkRich(node, "heading", linuxdoHeadingLevel(tag))
	case "blockquote":
		parser.walkRich(node, "quote", 0)
	case "pre":
		parser.addText("code", linuxdoNodeText(node), "")
	case "ul", "ol":
		parser.addList(node, tag == "ol")
	case "table":
		parser.addText("table", linuxdoTableText(node), "")
	case "iframe", "video", "audio":
		parser.addText("link", firstNonEmpty(linuxdoNodeAttr(node, "title"), strings.ToUpper(tag)), linuxdoNodeURL(node, parser.base))
	default:
		if linuxdoNodeHasClass(node, "poll") || linuxdoNodeAttr(node, "data-poll-name") != "" {
			if summary := linuxdoPollSummary(linuxdoRenderHTMLNode(node)); summary != "" {
				parser.addText("poll", summary, "")
				return
			}
		}
		if tag == "aside" && (linuxdoNodeHasClass(node, "quote") || linuxdoNodeHasClass(node, "onebox")) {
			parser.walkRich(node, "quote", 0)
			return
		}
		if linuxdoHasBlockChild(node) {
			parser.walkChildren(node)
			return
		}
		parser.walkRich(node, "text", 0)
	}
}

func (parser *linuxdoContentParser) walkRich(root *xhtml.Node, kind string, level int) {
	var text strings.Builder
	attachmentPending := false
	flush := func() {
		raw := text.String()
		text.Reset()
		if attachmentPending {
			suffix, remaining := linuxdoSplitAttachmentSuffix(raw)
			if suffix != "" {
				parser.appendAttachmentSuffix(suffix)
			}
			parser.addTextWithLevel(kind, remaining, "", level)
			attachmentPending = false
			return
		}
		parser.addTextWithLevel(kind, raw, "", level)
	}
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node == nil {
			return
		}
		switch node.Type {
		case xhtml.TextNode:
			text.WriteString(node.Data)
		case xhtml.ElementNode:
			if linuxdoNodeShouldSkip(node) {
				return
			}
			tag := strings.ToLower(node.Data)
			switch tag {
			case "script", "style", "noscript", "svg":
				return
			case "br":
				text.WriteByte('\n')
				return
			case "img":
				if linuxdoImageNodeLooksEmoji(node) {
					text.WriteString(firstNonEmpty(linuxdoNodeAttr(node, "title"), linuxdoNodeAttr(node, "alt")))
					return
				}
				flush()
				parser.addImage(node)
				return
			case "pre":
				flush()
				parser.addText("code", linuxdoNodeText(node), "")
				return
			case "blockquote":
				flush()
				parser.walkRich(node, "quote", 0)
				return
			case "ul", "ol":
				flush()
				parser.addList(node, tag == "ol")
				return
			case "a":
				if linuxdoNodeHasClass(node, "attachment") {
					flush()
					parser.addText("attachment", linuxdoNodeText(node), linuxdoNodeURL(node, parser.base))
					attachmentPending = true
					return
				}
			}
			for child := node.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
		}
	}
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		walk(child)
	}
	flush()
}

func (parser *linuxdoContentParser) addList(node *xhtml.Node, ordered bool) {
	lines := []string{}
	index := 1
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != xhtml.ElementNode || !strings.EqualFold(child.Data, "li") {
			continue
		}
		text := linuxdoCleanBlockText(linuxdoNodeText(child))
		if text == "" {
			continue
		}
		prefix := "• "
		if ordered {
			prefix = strings.TrimSpace(linuxdoOrdinal(index)) + " "
		}
		lines = append(lines, prefix+text)
		index++
	}
	parser.addText("list", strings.Join(lines, "\n"), "")
}

func linuxdoOrdinal(index int) string {
	if index <= 0 {
		return ""
	}
	return strconv.Itoa(index) + "."
}

func (parser *linuxdoContentParser) addText(kind, raw, rawURL string) {
	parser.addTextWithLevel(kind, raw, rawURL, 0)
}

func (parser *linuxdoContentParser) addTextWithLevel(kind, raw, rawURL string, level int) {
	text := linuxdoCleanBlockText(raw)
	if text == "" {
		return
	}
	block := linuxdoContentBlock{Kind: kind, Text: text, URL: strings.TrimSpace(rawURL), Level: level}
	if len(parser.blocks) > 0 && parser.blocks[len(parser.blocks)-1] == block {
		return
	}
	parser.blocks = append(parser.blocks, block)
}

func (parser *linuxdoContentParser) addDivider() {
	if len(parser.blocks) == 0 || parser.blocks[len(parser.blocks)-1].Kind == "divider" {
		return
	}
	parser.blocks = append(parser.blocks, linuxdoContentBlock{Kind: "divider"})
}

func (parser *linuxdoContentParser) appendAttachmentSuffix(raw string) {
	text := linuxdoCleanBlockText(raw)
	if text == "" || len(parser.blocks) == 0 {
		return
	}
	last := &parser.blocks[len(parser.blocks)-1]
	if last.Kind != "attachment" {
		return
	}
	last.Text = strings.TrimSpace(last.Text + " " + text)
}

func linuxdoSplitAttachmentSuffix(raw string) (string, string) {
	text := linuxdoCleanBlockText(raw)
	if text == "" {
		return "", ""
	}
	prefix := linuxdoAttachmentSizePrefixPattern.FindString(text)
	if prefix == "" {
		return "", text
	}
	return strings.TrimSpace(prefix), strings.TrimSpace(text[len(prefix):])
}

func (parser *linuxdoContentParser) addImage(node *xhtml.Node) {
	if node == nil || linuxdoImageNodeLooksEmoji(node) || linuxdoImageNodeLooksNonContent(node) {
		return
	}
	raw := ""
	for _, candidate := range []string{
		linuxdoNodeAttr(node, "data-orig-src"),
		linuxdoNodeAttr(node, "data-large-file"),
		linuxdoNodeAttr(node, "src"),
	} {
		candidate = strings.TrimSpace(stdhtml.UnescapeString(htmlUnescape(candidate)))
		candidate = linuxdoResolveContentURL(parser.base, candidate)
		if linuxdoUsableImage(candidate) {
			raw = candidate
			break
		}
	}
	if raw == "" {
		return
	}
	key := linuxdoImageDedupeKey(raw)
	if parser.seenImages[key] {
		return
	}
	parser.seenImages[key] = true
	parser.blocks = append(parser.blocks, linuxdoContentBlock{Kind: "image", URL: raw})
}

func linuxdoBlocksForRender(meta mediaMeta) []linuxdoContentBlock {
	base := firstNonEmpty(meta.URL, meta.SourceURL, linuxdoReferer)
	blocks := linuxdoBuildContentBlocks(meta.LinuxdoHTML, base)
	if len(blocks) == 0 {
		if text := linuxdoFallbackBodyText(meta.Desc); text != "" {
			blocks = append(blocks, linuxdoContentBlock{Kind: "text", Text: text})
		}
	}
	hasHTMLImage := false
	seen := map[string]bool{}
	for _, block := range blocks {
		if block.Kind == "image" {
			hasHTMLImage = true
			seen[linuxdoImageDedupeKey(block.URL)] = true
		}
	}
	// Linux.do 的 cooked HTML 保留了图片在正文中的准确位置。一旦内容树已经
	// 解析出图片，就不能再把媒体列表追加到末尾，否则不同 CDN/尺寸 URL 会让
	// 同一批图片整组重复；媒体列表只用于正文 HTML 无图时的降级展示。
	if !hasHTMLImage {
		for _, group := range meta.ImageURLs {
			if len(group) == 0 {
				continue
			}
			raw := strings.TrimSpace(group[0])
			key := linuxdoImageDedupeKey(raw)
			if raw == "" || seen[key] {
				continue
			}
			seen[key] = true
			blocks = append(blocks, linuxdoContentBlock{Kind: "image", URL: raw})
		}
	}
	if len(blocks) == 0 {
		return []linuxdoContentBlock{{Kind: "text", Text: "暂无正文摘要"}}
	}
	return blocks
}

func linuxdoPrimaryContentHTML(body string) string {
	for _, fragment := range linuxdoImageSourceFragments(body) {
		if len(linuxdoBuildContentBlocks(fragment, linuxdoReferer)) > 0 {
			return fragment
		}
	}
	return ""
}

func linuxdoFallbackBodyText(raw string) string {
	lines := []string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "[图片]" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func linuxdoCleanBlockText(raw string) string {
	raw = stdhtml.UnescapeString(htmlUnescape(raw))
	raw = strings.ReplaceAll(raw, "\u00a0", " ")
	raw = strings.ReplaceAll(raw, "\u200b", "")
	lines := []string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			lines = append(lines, line)
		}
	}
	raw = strings.Join(lines, "\n")
	raw = linuxdoStripPromotionDeclarations(raw)
	return strings.TrimSpace(raw)
}

func linuxdoNodeText(node *xhtml.Node) string {
	var text strings.Builder
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current == nil {
			return
		}
		if current.Type == xhtml.TextNode {
			text.WriteString(current.Data)
			return
		}
		if current.Type != xhtml.ElementNode {
			for child := current.FirstChild; child != nil; child = child.NextSibling {
				walk(child)
			}
			return
		}
		if linuxdoNodeShouldSkip(current) {
			return
		}
		tag := strings.ToLower(current.Data)
		if tag == "script" || tag == "style" || tag == "noscript" || tag == "svg" {
			return
		}
		if tag == "br" {
			text.WriteByte('\n')
			return
		}
		if tag == "img" && linuxdoImageNodeLooksEmoji(current) {
			text.WriteString(firstNonEmpty(linuxdoNodeAttr(current, "title"), linuxdoNodeAttr(current, "alt")))
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if tag == "p" || tag == "div" || tag == "li" || tag == "tr" || tag == "blockquote" {
			text.WriteByte('\n')
		}
	}
	walk(node)
	return text.String()
}

func linuxdoTableText(node *xhtml.Node) string {
	rows := []string{}
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current.Type == xhtml.ElementNode && strings.EqualFold(current.Data, "tr") {
			cells := []string{}
			for cell := current.FirstChild; cell != nil; cell = cell.NextSibling {
				if cell.Type == xhtml.ElementNode && (strings.EqualFold(cell.Data, "td") || strings.EqualFold(cell.Data, "th")) {
					if text := linuxdoCleanBlockText(linuxdoNodeText(cell)); text != "" {
						cells = append(cells, text)
					}
				}
			}
			if len(cells) > 0 {
				rows = append(rows, strings.Join(cells, "  |  "))
			}
			return
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(rows, "\n")
}

func linuxdoHasBlockChild(node *xhtml.Node) bool {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type != xhtml.ElementNode {
			continue
		}
		if linuxdoNodeShouldSkip(child) {
			continue
		}
		switch strings.ToLower(child.Data) {
		case "p", "div", "section", "article", "aside", "figure", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "ul", "ol", "table", "hr":
			return true
		}
	}
	return false
}

func linuxdoHeadingLevel(tag string) int {
	if len(tag) != 2 || tag[0] != 'h' || tag[1] < '1' || tag[1] > '6' {
		return 4
	}
	return int(tag[1] - '0')
}

func linuxdoNodeShouldSkip(node *xhtml.Node) bool {
	if node == nil || node.Type != xhtml.ElementNode {
		return false
	}
	if strings.EqualFold(node.Data, "a") && linuxdoNodeHasClass(node, "anchor") {
		return true
	}
	for _, className := range []string{"meta", "d-icon", "lb-spacer", "cooked-selection-barrier"} {
		if linuxdoNodeHasClass(node, className) {
			return true
		}
	}
	return false
}

func linuxdoImageNodeLooksEmoji(node *xhtml.Node) bool {
	className := strings.ToLower(linuxdoNodeAttr(node, "class"))
	return strings.Contains(className, "emoji") || strings.Contains(className, "emoticon") || strings.Contains(className, "smiley")
}

func linuxdoImageNodeLooksNonContent(node *xhtml.Node) bool {
	return linuxdoImageTagLooksNonContent(linuxdoRenderHTMLNode(node))
}

func linuxdoNodeHasClass(node *xhtml.Node, className string) bool {
	want := strings.ToLower(strings.TrimSpace(className))
	for _, item := range strings.Fields(strings.ToLower(linuxdoNodeAttr(node, "class"))) {
		if item == want {
			return true
		}
	}
	return false
}

func linuxdoNodeAttr(node *xhtml.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return strings.TrimSpace(attr.Val)
		}
	}
	return ""
}

func linuxdoNodeURL(node *xhtml.Node, base string) string {
	raw := firstNonEmpty(linuxdoNodeAttr(node, "href"), linuxdoNodeAttr(node, "src"))
	raw = strings.TrimSpace(stdhtml.UnescapeString(htmlUnescape(raw)))
	return linuxdoResolveContentURL(base, raw)
}

func linuxdoResolveContentURL(base, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return raw
	}
	return absolutize(base, raw)
}

func linuxdoRenderHTMLNode(node *xhtml.Node) string {
	var out strings.Builder
	if err := xhtml.Render(&out, node); err != nil {
		return ""
	}
	return out.String()
}
