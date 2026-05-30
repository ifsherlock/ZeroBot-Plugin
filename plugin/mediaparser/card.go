package mediaparser

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FloatTech/floatbox/file"
	"github.com/FloatTech/gg"
	"github.com/FloatTech/zbputils/control"
	"github.com/FloatTech/zbputils/img/text"
	"github.com/disintegration/imaging"
	"github.com/sirupsen/logrus"
	_ "golang.org/x/image/webp"
)

const (
	cardWidth    = 1248
	cardPad      = 44
	cardOuterPad = 44
	cardContent  = cardWidth - cardPad*2
)

type cardPalette struct {
	LogoText color.RGBA
	Title    color.RGBA
}

func renderInfoCard(meta mediaMeta) (string, error) {
	fontBytes, err := file.GetLazyData(text.GlowSansFontFile, control.Md5File, true)
	if err != nil {
		return "", err
	}
	if meta.Platform == "keylol" {
		return renderKeylolThreadCard(meta, fontBytes)
	}
	if shouldConsiderLongImageCard(meta) {
		return renderLongImageCard(meta, fontBytes)
	}
	if shouldRenderAsGalleryCard(meta) {
		return renderGalleryCard(meta, fontBytes)
	}
	return renderVideoCard(meta, fontBytes)
}

func keylolBodyFontBytes(fallback []byte) []byte {
	fontBytes, err := file.GetLazyData(text.FontFile, control.Md5File, true)
	if err != nil || len(fontBytes) == 0 {
		return fallback
	}
	return fontBytes
}

func shouldRenderAsGalleryCard(meta mediaMeta) bool {
	if len(meta.ImageURLs) == 0 {
		return false
	}
	if len(meta.VideoURLs) == 0 {
		return true
	}
	return isCombinedMediaPlatform(meta.Platform) && hasMixedMediaItems(meta)
}

func shouldConsiderLongImageCard(meta mediaMeta) bool {
	return len(meta.VideoURLs) == 0 && len(meta.ImageURLs) == 1 && len(meta.ImageURLs[0]) > 0 && strings.TrimSpace(meta.Desc) != ""
}

func renderLongImageCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	avatarImg := fetchCardImage(meta.Avatar, meta.ImageHeads)
	img := fetchCardImage(meta.ImageURLs[0][0], meta.ImageHeads)
	if !shouldRenderLongImageCard(meta, img) {
		return renderGalleryCard(meta, fontBytes)
	}

	titleLines := []string{}

	outerPad := cardOuterPad
	panelX, panelY := outerPad, outerPad
	panelW := cardWidth - outerPad*2
	headerH := 164
	contentPad := 28
	headerTop := float64(panelY + 18)
	contentX := panelX + contentPad
	contentW := panelW - contentPad*2
	bodyLines := wrapTextByPixels(gg.NewContext(cardWidth, 100), bodyFontBytes, 30, meta.Desc, float64(contentW))
	if len(bodyLines) > 18 {
		bodyLines = bodyLines[:18]
	}
	bodyY := float64(panelY + headerH + 62)
	imageY := bodyY + float64(len(titleLines))*58 + float64(len(bodyLines))*48 + 52
	if len(titleLines) > 0 {
		imageY += 20
	}
	imageH := longImageCardHeight(img, contentW)
	panelH := int(imageY) + imageH + 54 - panelY
	height := panelY + panelH + outerPad

	dc := gg.NewContext(cardWidth, height)
	dc.SetRGB255(190, 226, 248)
	dc.Clear()
	drawGalleryPanel(dc, panelX, panelY, panelW, panelH)
	drawGlassHeader(dc, fontBytes, meta.Platform, panelX, panelY, panelW, headerH)

	displayAuthor := cardDisplayAuthor(meta.Author)
	drawAvatarWithBorder(dc, fontBytes, avatarImg, panelX+42, int(headerTop), 124, displayAuthor)
	drawShadowText(dc, fontBytes, 40, truncate(firstNonEmpty(displayAuthor, "鏈煡鐢ㄦ埛"), 22), float64(panelX+198), headerTop+39)
	if meta.Timestamp != "" {
		drawShadowText(dc, fontBytes, 30, meta.Timestamp, float64(panelX+198), headerTop+88)
	}
	drawPlatformLogo(dc, fontBytes, meta.Platform, float64(panelX+panelW-34), headerTop+50)

	y := bodyY
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, 42, platformPalette(meta.Platform).Title, line, float64(contentX), y)
		y += 58
	}
	if len(titleLines) > 0 {
		y += 20
	}
	for _, line := range bodyLines {
		drawTopicLine(dc, bodyFontBytes, 30, line, float64(contentX), y)
		y += 48
	}

	drawFloatingImageCellContain(dc, img, contentX, int(imageY), contentW, imageH)
	return saveCardPNG(dc, meta)
}

func shouldRenderLongImageCard(meta mediaMeta, img image.Image) bool {
	if len(meta.VideoURLs) != 0 || len(meta.ImageURLs) != 1 || len(meta.ImageURLs[0]) == 0 || strings.TrimSpace(meta.Desc) == "" {
		return false
	}
	return !isExtremeTallImage(img)
}

func isExtremeTallImage(img image.Image) bool {
	if img == nil {
		return false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return false
	}
	return float64(b.Dy())/float64(b.Dx()) > 2.4
}

func renderVideoCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	coverImg := fetchCardImage(firstCardCover(meta), meta.ImageHeads)
	avatarImg := fetchCardImage(meta.Avatar, meta.ImageHeads)

	title := firstNonEmpty(meta.Title, meta.Desc, "媒体解析")
	titleLines := []string{}
	summaryLines := []string{}
	if len(summaryLines) == 0 {
		summaryLines = []string{"该视频暂无总结"}
	}

	outerPad := cardOuterPad
	panelX, panelY := outerPad, outerPad
	panelW := cardWidth - outerPad*2
	headerH := 164
	contentPad := 28
	headerTop := float64(panelY + 18)
	contentX := panelX + contentPad
	contentW := panelW - contentPad*2
	titleLines = wrapDisplayTextByPixels(fontBytes, 42, title, float64(contentW), 2)
	if len(titleLines) == 0 {
		titleLines = []string{title}
	}
	summaryLines = wrapTextByPixels(gg.NewContext(cardWidth, 100), bodyFontBytes, 30, cardSummaryText(meta), float64(contentW))
	if len(summaryLines) > 5 {
		summaryLines = summaryLines[:5]
	}
	if len(summaryLines) == 0 {
		summaryLines = []string{"该视频暂无总结"}
	}
	titleY := float64(panelY + headerH + 64)
	coverY := titleY + float64(len(titleLines))*58 + 24
	coverH := cardCoverHeightForWidth(coverImg, contentW)
	summaryY := coverY + float64(coverH) + 68
	panelH := int(summaryY) + len(summaryLines)*48 + 64 - panelY
	if panelH < int(summaryY)+80-panelY {
		panelH = int(summaryY) + 80 - panelY
	}
	height := panelY + panelH + outerPad

	dc := gg.NewContext(cardWidth, height)
	dc.SetRGB255(190, 226, 248)
	dc.Clear()
	drawGalleryPanel(dc, panelX, panelY, panelW, panelH)
	drawGlassHeader(dc, fontBytes, meta.Platform, panelX, panelY, panelW, headerH)

	displayAuthor := cardDisplayAuthor(meta.Author)
	drawAvatarWithBorder(dc, fontBytes, avatarImg, panelX+42, int(headerTop), 124, displayAuthor)
	drawShadowText(dc, fontBytes, 40, truncate(firstNonEmpty(displayAuthor, "未知用户"), 22), float64(panelX+198), headerTop+39)
	if meta.Timestamp != "" {
		drawShadowText(dc, fontBytes, 30, meta.Timestamp, float64(panelX+198), headerTop+88)
	}
	drawPlatformLogo(dc, fontBytes, meta.Platform, float64(panelX+panelW-34), headerTop+50)

	y := titleY + 20
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, 42, platformPalette(meta.Platform).Title, line, float64(contentX), y)
		y += 58
	}

	drawFloatingCoverCell(dc, coverImg, contentX, int(coverY), contentW, coverH, shouldDrawPlayOverlay(meta))

	y = summaryY
	for _, line := range summaryLines {
		drawTopicLine(dc, bodyFontBytes, 30, line, float64(contentX), y)
		y += 48
	}

	return saveCardPNG(dc, meta)
}

func renderGalleryCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	avatarImg := fetchCardImage(meta.Avatar, meta.ImageHeads)
	imageURLs := make([]string, 0, len(meta.ImageURLs))
	for _, group := range meta.ImageURLs {
		if len(group) == 0 {
			continue
		}
		imageURLs = append(imageURLs, group[0])
		if len(imageURLs) >= 9 {
			break
		}
	}
	images := compactCardImages(fetchCardImages(imageURLs, meta.ImageHeads))

	title := firstNonEmpty(meta.Title, "媒体解析")
	titleLines := []string{}
	descLines := []string{}

	outerPad := cardOuterPad
	panelX, panelY := outerPad, outerPad
	panelW := cardWidth - outerPad*2
	headerH := 164
	contentPad := 28
	headerTop := float64(panelY + 18)
	galleryX := panelX + contentPad
	galleryW := panelW - contentPad*2
	titleLines = wrapDisplayTextByPixels(fontBytes, 42, title, float64(galleryW), 2)
	if len(titleLines) == 0 {
		titleLines = []string{title}
	}
	descLines = wrapTextByPixels(gg.NewContext(cardWidth, 100), bodyFontBytes, 30, meta.Desc, float64(galleryW))
	if len(descLines) > 14 {
		descLines = descLines[:14]
	}
	titleY := float64(panelY + headerH + 64)
	gridY := titleY + float64(len(titleLines))*58 + 34
	gridH := galleryGridHeightForImages(images, galleryW)
	descY := gridY
	if gridH > 0 {
		descY = gridY + float64(gridH) + 70
	}
	panelH := int(descY) + len(descLines)*48 + 70 - panelY
	if panelH < int(descY)+80-panelY {
		panelH = int(descY) + 80 - panelY
	}
	height := panelY + panelH + outerPad

	dc := gg.NewContext(cardWidth, height)
	dc.SetRGB255(190, 226, 248)
	dc.Clear()
	drawGalleryPanel(dc, panelX, panelY, panelW, panelH)
	drawGlassHeader(dc, fontBytes, meta.Platform, panelX, panelY, panelW, headerH)

	displayAuthor := cardDisplayAuthor(meta.Author)
	drawAvatarWithBorder(dc, fontBytes, avatarImg, panelX+42, int(headerTop), 124, displayAuthor)
	drawShadowText(dc, fontBytes, 40, truncate(firstNonEmpty(displayAuthor, "未知用户"), 22), float64(panelX+198), headerTop+39)
	if meta.Timestamp != "" {
		drawShadowText(dc, fontBytes, 30, meta.Timestamp, float64(panelX+198), headerTop+88)
	}
	drawPlatformLogo(dc, fontBytes, meta.Platform, float64(panelX+panelW-34), headerTop+50)

	y := titleY
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, 42, platformPalette(meta.Platform).Title, line, float64(galleryX), y)
		y += 58
	}

	drawGalleryGrid(dc, images, galleryX, int(gridY), galleryW, gridH)

	y = descY
	for _, line := range descLines {
		drawTopicLine(dc, bodyFontBytes, 30, line, float64(galleryX), y)
		y += 48
	}

	return saveCardPNG(dc, meta)
}

type keylolRenderBlock struct {
	kind   string
	text   string
	url    string
	title  string
	desc   string
	cover  string
	img    image.Image
	width  int
	imgH   int
	height int
	lines  []string
}

func renderKeylolThreadCard(meta mediaMeta, fontBytes []byte) (string, error) {
	const (
		w         = 1320
		outerPad  = 32
		panelPad  = 50
		contentW  = w - outerPad*2 - panelPad*2
		titleSize = 38.0
		bodySize  = 27.0
		titleLH   = 56
		bodyLH    = 42
		imageGap  = 28
		blockGap  = 22
	)
	theme := keylolCardThemeNow()
	blocks := keylolBlocksForRender(meta)
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	dcMeasure := gg.NewContext(w, 100)
	mustFont(dcMeasure, bodyFontBytes, bodySize)
	renderBlocks := make([]keylolRenderBlock, 0, len(blocks))
	for i := 0; i < len(blocks); i++ {
		block := blocks[i]
		switch block.Kind {
		case "image":
			img := keylolPrepareImage(fetchCardImage(block.URL, meta.ImageHeads))
			iw, ih := keylolImageDrawSize(img, contentW)
			renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "image", url: block.URL, img: img, width: iw, height: ih})
		case "inline_image":
			img := keylolPrepareImage(fetchCardImage(block.URL, meta.ImageHeads))
			iw, ih := keylolInlineImageDrawSize(img)
			if i+1 < len(blocks) && (blocks[i+1].Kind == "text" || blocks[i+1].Kind == "heading2") {
				next := blocks[i+1]
				size := bodySize
				lineH := bodyLH
				kind := "inline_image_text"
				if next.Kind == "heading2" {
					size = 29
					lineH = 42
					kind = "inline_image_heading"
				}
				lines := wrapTextByPixels(dcMeasure, bodyFontBytes, size, next.Text, float64(contentW-iw-18))
				if len(lines) > 0 {
					renderBlocks = append(renderBlocks, keylolRenderBlock{kind: kind, text: next.Text, url: block.URL, img: img, width: iw, imgH: ih, height: maxInt(ih, len(lines)*lineH), lines: lines})
					i++
					continue
				}
			}
			renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "inline_image", url: block.URL, img: img, width: iw, height: ih})
		case "heading1":
			lines := wrapTextByPixels(dcMeasure, fontBytes, 34, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "heading1", text: block.Text, lines: lines, height: len(lines)*48 + 18})
			}
		case "heading2":
			lines := wrapTextByPixels(dcMeasure, fontBytes, 29, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "heading2", text: block.Text, lines: lines, height: len(lines) * 42})
			}
		case "collapse":
			lines := wrapTextByPixels(dcMeasure, fontBytes, 24, block.Text, float64(contentW-64))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "collapse", text: block.Text, lines: lines, height: maxInt(48, len(lines)*34+14)})
			}
		case "steam_card":
			img := keylolPrepareImage(fetchCardImage(block.Cover, nil))
			descLines := wrapTextByPixels(dcMeasure, bodyFontBytes, 20, block.Desc, float64(contentW-330))
			if len(descLines) > 2 {
				descLines = descLines[:2]
			}
			renderBlocks = append(renderBlocks, keylolRenderBlock{
				kind:   "steam_card",
				url:    block.URL,
				title:  firstNonEmpty(block.Title, "Steam 游戏"),
				desc:   block.Desc,
				cover:  block.Cover,
				img:    img,
				lines:  descLines,
				height: 166,
			})
		case "asf_link":
			renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "asf_link", title: block.Title, height: 38})
		case "text":
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, bodySize, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "text", text: block.Text, lines: lines, height: len(lines) * bodyLH})
			}
		}
	}
	titleLines := wrapTextByPixels(dcMeasure, fontBytes, titleSize, firstNonEmpty(meta.Title, "Keylol 帖子"), float64(contentW-150))
	contentH := 0
	for _, block := range renderBlocks {
		if contentH > 0 {
			contentH += blockGap
		}
		contentH += block.height
		if block.kind == "image" {
			contentH += imageGap
		}
	}
	if contentH < 120 {
		contentH = 120
	}
	headerH := 86 + len(titleLines)*titleLH + 72
	footerH := 112
	panelH := panelPad + headerH + contentH + footerH
	height := panelH + outerPad*2

	dc := gg.NewContext(w, height)
	setRGB(dc, theme.BG)
	dc.Clear()
	drawKeylolPanel(dc, outerPad, outerPad, w-outerPad*2, panelH, theme)
	x := outerPad + panelPad
	y := outerPad + panelPad
	drawKeylolBadge(dc, fontBytes, float64(x), float64(y+10))
	if meta.Timestamp != "" {
		mustFont(dc, fontBytes, 22)
		setRGB(dc, theme.Muted)
		dc.DrawStringAnchored(meta.Timestamp, float64(w-outerPad-panelPad), float64(y+28), 1, 0.5)
	}
	y += 74
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, titleSize, theme.Title, line, float64(x), float64(y))
		y += titleLH
	}
	authorLine := firstNonEmpty(meta.Author, "Keylol 用户")
	drawKeylolAuthorLine(dc, fontBytes, authorLine, x, y+6)
	y += 48
	setRGB(dc, theme.Line)
	dc.DrawRectangle(float64(x), float64(y), float64(contentW), 2)
	dc.Fill()
	y += 44
	for _, block := range renderBlocks {
		switch block.kind {
		case "text":
			for _, line := range block.lines {
				c := theme.Body
				if keylolLooksLikeLink(line) {
					c = color.RGBA{R: 0, G: 102, B: 204, A: 255}
				}
				drawInlineEmoji(dc, bodyFontBytes, bodySize, c, line, float64(x), float64(y))
				y += bodyLH
			}
		case "image":
			ix := x
			if block.width < contentW {
				ix = x + (contentW-block.width)/2
			}
			drawKeylolFullImage(dc, block.img, ix, y, block.width, block.height)
			y += block.height + imageGap
		case "inline_image":
			drawKeylolInlineImage(dc, block.img, x, y, block.width, block.height)
			y += block.height
		case "inline_image_text", "inline_image_heading":
			imgY := y + (block.height-block.imgH)/2
			drawKeylolInlineImage(dc, block.img, x, imgY, block.width, block.imgH)
			textX := x + block.width + 16
			yy := y + 2
			size := bodySize
			lineH := bodyLH
			c := theme.Body
			if block.kind == "inline_image_heading" {
				size = 29
				lineH = 42
				c = theme.Title
			}
			for _, line := range block.lines {
				drawInlineEmoji(dc, bodyFontBytes, size, c, line, float64(textX), float64(yy))
				yy += lineH
			}
			y += block.height
		case "heading1":
			y = drawKeylolHeading1(dc, fontBytes, block.lines, x, y, contentW)
		case "heading2":
			for _, line := range block.lines {
				drawInlineEmoji(dc, fontBytes, 29, theme.Title, line, float64(x), float64(y))
				y += 42
			}
		case "collapse":
			drawKeylolCollapse(dc, fontBytes, block.lines, x, y, contentW, block.height)
			y += block.height
		case "steam_card":
			drawKeylolSteamCard(dc, fontBytes, block, x, y, contentW, block.height)
			y += block.height
		case "asf_link":
			drawKeylolASFLink(dc, fontBytes, block.title, x, y)
			y += block.height
		}
		y += blockGap
	}
	mustFont(dc, fontBytes, 20)
	setRGB(dc, theme.Footer)
	dc.DrawStringAnchored(keylolFooterLine(meta), float64(w)/2, float64(height-outerPad-34), 0.5, 0.5)
	return saveCardPNG(dc, meta)
}

func keylolFooterLine(meta mediaMeta) string {
	cfg := snapshotConfig()
	tpl := strings.TrimSpace(cfg.KeylolFooter)
	if tpl == "" {
		tpl = "Keylol 帖子截图 · 浏览器渲染 · {time}"
	}
	now := time.Now().Format("2006-01-02 15:04")
	replacer := strings.NewReplacer(
		"{time}", now,
		"{title}", strings.TrimSpace(meta.Title),
		"{author}", strings.TrimSpace(meta.Author),
	)
	return strings.TrimSpace(replacer.Replace(tpl))
}

func keylolBlocksForRender(meta mediaMeta) []keylolBlock {
	if len(meta.KeylolBlocks) > 0 {
		return meta.KeylolBlocks
	}
	blocks := []keylolBlock{}
	if strings.TrimSpace(meta.Desc) != "" {
		blocks = append(blocks, keylolBlock{Kind: "text", Text: strings.TrimSpace(meta.Desc)})
	}
	for _, group := range meta.ImageURLs {
		if len(group) > 0 && group[0] != "" {
			blocks = append(blocks, keylolBlock{Kind: "image", URL: group[0]})
		}
	}
	return blocks
}

func keylolImageDrawSize(img image.Image, maxW int) (int, int) {
	if img == nil {
		return maxW, maxW * 9 / 16
	}
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	if iw <= 0 || ih <= 0 {
		return maxW, maxW * 9 / 16
	}
	if iw <= maxW {
		return iw, ih
	}
	h := int(float64(ih) * float64(maxW) / float64(iw))
	return maxW, maxInt(h, 1)
}

func keylolInlineImageDrawSize(img image.Image) (int, int) {
	if img == nil {
		return 120, 64
	}
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	if iw <= 0 || ih <= 0 {
		return 120, 64
	}
	maxH := 54
	if ih > maxH {
		iw = int(float64(iw) * float64(maxH) / float64(ih))
		ih = maxH
	}
	if iw > 160 {
		ih = int(float64(ih) * 160 / float64(iw))
		iw = 160
	}
	return maxInt(iw, 1), maxInt(ih, 1)
}

type keylolCardTheme struct {
	BG     color.RGBA
	Panel  color.RGBA
	Title  color.RGBA
	Body   color.RGBA
	Muted  color.RGBA
	Line   color.RGBA
	Footer color.RGBA
}

func keylolCardThemeNow() keylolCardTheme {
	hour := time.Now().Hour()
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		hour = time.Now().In(loc).Hour()
	}
	if hour >= 18 || hour < 6 {
		return keylolCardTheme{
			BG:     color.RGBA{R: 7, G: 12, B: 20, A: 255},
			Panel:  color.RGBA{R: 18, G: 24, B: 34, A: 255},
			Title:  color.RGBA{R: 230, G: 235, B: 244, A: 255},
			Body:   color.RGBA{R: 184, G: 190, B: 201, A: 255},
			Muted:  color.RGBA{R: 127, G: 137, B: 153, A: 255},
			Line:   color.RGBA{R: 42, G: 50, B: 62, A: 255},
			Footer: color.RGBA{R: 104, G: 115, B: 132, A: 255},
		}
	}
	return keylolCardTheme{
		BG:     color.RGBA{R: 234, G: 238, B: 244, A: 255},
		Panel:  color.RGBA{R: 255, G: 255, B: 255, A: 255},
		Title:  color.RGBA{R: 26, G: 31, B: 48, A: 255},
		Body:   color.RGBA{R: 45, G: 48, B: 56, A: 255},
		Muted:  color.RGBA{R: 145, G: 150, B: 160, A: 255},
		Line:   color.RGBA{R: 224, G: 228, B: 235, A: 255},
		Footer: color.RGBA{R: 176, G: 184, B: 194, A: 255},
	}
}

func setRGB(dc *gg.Context, c color.RGBA) {
	dc.SetRGB255(int(c.R), int(c.G), int(c.B))
}

func drawKeylolPanel(dc *gg.Context, x, y, w, h int, theme keylolCardTheme) {
	for i := 14; i >= 1; i-- {
		dc.SetRGBA255(0, 0, 0, 3+i*2)
		dc.DrawRoundedRectangle(float64(x), float64(y+i), float64(w), float64(h), 18)
		dc.Fill()
	}
	setRGB(dc, theme.Panel)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 18)
	dc.Fill()
}

func drawKeylolBadge(dc *gg.Context, fontBytes []byte, x, y float64) {
	dc.SetRGB255(74, 137, 218)
	dc.DrawRoundedRectangle(x, y, 132, 40, 16)
	dc.Fill()
	drawInlineEmoji(dc, fontBytes, 20, color.RGBA{R: 255, G: 255, B: 255, A: 255}, "🎮 Keylol", x+16, y+21)
}

func drawKeylolAuthorLine(dc *gg.Context, fontBytes []byte, author string, x, y int) {
	cursor := drawInlineEmoji(dc, fontBytes, 23, color.RGBA{R: 50, G: 124, B: 214, A: 255}, "✍️", float64(x), float64(y))
	drawInlineEmoji(dc, fontBytes, 24, color.RGBA{R: 50, G: 124, B: 214, A: 255}, author, float64(x)+cursor+10, float64(y))
}

func drawKeylolFullImage(dc *gg.Context, img image.Image, x, y, w, h int) {
	dc.SetRGB255(255, 255, 255)
	dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
	dc.Fill()
	if img == nil {
		dc.SetRGB255(238, 241, 245)
		dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
		dc.Fill()
	} else {
		dc.DrawImage(imaging.Resize(img, w, h, imaging.Lanczos), x, y)
	}
	dc.SetRGB255(220, 225, 232)
	dc.SetLineWidth(1)
	dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
	dc.Stroke()
}

func drawKeylolInlineImage(dc *gg.Context, img image.Image, x, y, w, h int) {
	if img == nil {
		return
	}
	dc.DrawImage(imaging.Resize(img, w, h, imaging.Lanczos), x, y)
}

func drawKeylolHeading1(dc *gg.Context, fontBytes []byte, lines []string, x, y, w int) int {
	for _, line := range lines {
		drawInlineEmoji(dc, fontBytes, 34, color.RGBA{R: 68, G: 68, B: 68, A: 255}, line, float64(x), float64(y))
		y += 48
	}
	lineY := y - 10
	dc.SetRGB255(85, 85, 85)
	dc.SetLineWidth(3)
	dc.DrawLine(float64(x), float64(lineY), float64(x+w), float64(lineY))
	dc.Stroke()
	dc.DrawLine(float64(x), float64(lineY+8), float64(x+w), float64(lineY+8))
	dc.Stroke()
	return lineY + 24
}

func drawKeylolCollapse(dc *gg.Context, fontBytes []byte, lines []string, x, y, w, h int) {
	dc.SetRGB255(102, 187, 255)
	dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
	dc.Fill()
	dc.SetRGB255(87, 186, 232)
	dc.SetLineWidth(1)
	dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
	dc.Stroke()
	mustFont(dc, fontBytes, 24)
	dc.SetRGB255(255, 255, 255)
	dc.DrawStringAnchored(">", float64(x+22), float64(y+24), 0.5, 0.5)
	yy := y + 31
	for _, line := range lines {
		drawInlineEmoji(dc, fontBytes, 24, color.RGBA{R: 255, G: 255, B: 255, A: 255}, line, float64(x+46), float64(yy))
		yy += 34
	}
}

func drawKeylolSteamCard(dc *gg.Context, fontBytes []byte, block keylolRenderBlock, x, y, w, h int) {
	fx, fy := float64(x), float64(y)
	for i := 8; i >= 1; i-- {
		dc.SetRGBA255(10, 18, 32, 8+i*3)
		dc.DrawRoundedRectangle(fx, fy+float64(i), float64(w), float64(h), 16)
		dc.Fill()
	}
	dc.SetRGB255(20, 34, 54)
	dc.DrawRoundedRectangle(fx, fy, float64(w), float64(h), 16)
	dc.Fill()
	dc.SetRGBA255(92, 136, 184, 68)
	dc.SetLineWidth(1)
	dc.DrawRoundedRectangle(fx+0.5, fy+0.5, float64(w)-1, float64(h)-1, 16)
	dc.Stroke()

	coverW := 248
	coverH := h - 28
	coverX := x + 16
	coverY := y + 14
	dc.DrawRoundedRectangle(float64(coverX), float64(coverY), float64(coverW), float64(coverH), 10)
	dc.ClipPreserve()
	if block.img == nil {
		dc.SetRGB255(31, 45, 65)
		dc.Fill()
		mustFont(dc, fontBytes, 28)
		dc.SetRGB255(145, 178, 214)
		dc.DrawStringAnchored("Steam", float64(coverX+coverW/2), float64(coverY+coverH/2+9), 0.5, 0.5)
	} else {
		dc.DrawImage(imaging.Fit(block.img, coverW, coverH, imaging.Lanczos), coverX, coverY)
	}
	dc.ResetClip()

	textX := x + 288
	title := truncate(firstNonEmpty(block.title, "Steam 游戏"), 34)
	drawInlineEmoji(dc, fontBytes, 25, color.RGBA{R: 237, G: 244, B: 255, A: 255}, "Steam 上的 "+title, float64(textX), float64(y+42))
	yy := y + 78
	for _, line := range block.lines {
		drawInlineEmoji(dc, keylolBodyFontBytes(fontBytes), 20, color.RGBA{R: 166, G: 188, B: 214, A: 255}, truncate(line, 56), float64(textX), float64(yy))
		yy += 28
	}
	mustFont(dc, fontBytes, 19)
	dc.SetRGB255(95, 177, 235)
	dc.DrawStringAnchored("在 Steam 上查看 →", float64(textX), float64(y+h-28), 0, 0.5)
}

func drawKeylolASFLink(dc *gg.Context, fontBytes []byte, appID string, x, y int) {
	drawInlineEmoji(dc, fontBytes, 24, color.RGBA{R: 58, G: 166, B: 230, A: 255}, "🎮 Steam "+strings.TrimSpace(appID)+" →", float64(x), float64(y+24))
}

func keylolPrepareImage(img image.Image) image.Image {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Over)
	for y := out.Bounds().Min.Y; y < out.Bounds().Max.Y; y++ {
		for x := out.Bounds().Min.X; x < out.Bounds().Max.X; x++ {
			r, g, b, a := out.At(x, y).RGBA()
			if a == 0 {
				out.Set(x, y, color.White)
				continue
			}
			rr, gg, bb := uint8(r>>8), uint8(g>>8), uint8(b>>8)
			maxc := maxUint8(rr, gg, bb)
			minc := minUint8(rr, gg, bb)
			if minc >= 224 && maxc-minc <= 24 {
				out.Set(x, y, color.White)
			}
		}
	}
	return out
}

func maxUint8(a, b, c uint8) uint8 {
	if b > a {
		a = b
	}
	if c > a {
		a = c
	}
	return a
}

func minUint8(a, b, c uint8) uint8 {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

func wrapTextByPixels(dc *gg.Context, fontBytes []byte, size float64, s string, maxW float64) []string {
	mustFont(dc, fontBytes, size)
	out := []string{}
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		line := ""
		for _, r := range []rune(para) {
			next := line + string(r)
			if ww, _ := dc.MeasureString(next); ww > maxW && line != "" {
				out = append(out, line)
				line = string(r)
			} else {
				line = next
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return fixWrappedLinePunctuation(out)
}

func keylolLooksLikeLink(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") || strings.Contains(lower, "store.steampowered.com")
}

func saveCardPNG(dc *gg.Context, meta mediaMeta) (string, error) {
	out := filepath.Join(cacheDir, fmt.Sprintf("card_%s.png", cacheName(meta.SourceURL, meta.Platform)))
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return "", err
	}
	if err := dc.SavePNG(out); err != nil {
		return "", err
	}
	logrus.Infof("[mediaparser] render_card_ok platform=%s gallery_style=v3 path=%s", meta.Platform, out)
	return out, nil
}

func mustFont(dc *gg.Context, fontBytes []byte, size float64) {
	_ = dc.ParseFontFace(fontBytes, size)
}

func firstCardCover(meta mediaMeta) string {
	if meta.Cover != "" {
		return meta.Cover
	}
	if len(meta.ImageURLs) > 0 && len(meta.ImageURLs[0]) > 0 {
		return meta.ImageURLs[0][0]
	}
	return ""
}

func cardCoverHeight(img image.Image) int {
	return cardCoverHeightForWidth(img, cardContent)
}

func cardCoverHeightForWidth(img image.Image, w int) int {
	if img == nil {
		return 656
	}
	b := img.Bounds()
	iw, ih := b.Dx(), b.Dy()
	if iw <= 0 || ih <= 0 {
		return 656
	}
	out := int(float64(w) * float64(ih) / float64(iw))
	if out < 520 {
		return 520
	}
	if out > 760 {
		return 760
	}
	return out
}

func cardSummaryText(meta mediaMeta) string {
	if strings.TrimSpace(meta.Desc) != "" {
		return strings.TrimSpace(meta.Desc)
	}
	if meta.Error != "" {
		return "解析失败：" + meta.Error
	}
	if len(meta.VideoSkipReasons) > 0 && meta.VideoSkipReasons[0] != "" {
		return meta.VideoSkipReasons[0]
	}
	if len(meta.ImageURLs) > 0 && len(meta.VideoURLs) == 0 {
		return "图文内容"
	}
	return "该视频暂无总结"
}

func fetchCardImage(raw string, headers map[string]string) image.Image {
	raw = normalizeCardImageURL(raw)
	if raw == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return nil
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if strings.Contains(req.Header.Get("Accept"), "image/avif") {
		req.Header.Set("Accept", "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", defaultUA)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	}
	c := &http.Client{Timeout: 18 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 18<<20))
	if err != nil {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	return img
}

func fetchCardImages(urls []string, headers map[string]string) []image.Image {
	if len(urls) == 0 {
		return nil
	}
	out := make([]image.Image, len(urls))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, raw := range urls {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = fetchCardImage(raw, headers)
		}(i, raw)
	}
	wg.Wait()
	return compactCardImages(out)
}

func compactCardImages(imgs []image.Image) []image.Image {
	compact := make([]image.Image, 0, len(imgs))
	for _, img := range imgs {
		if img != nil && !isBlankCardImage(img) {
			compact = append(compact, img)
		}
	}
	return compact
}

func isBlankCardImage(img image.Image) bool {
	if img == nil {
		return true
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return true
	}
	stepX := maxInt(b.Dx()/8, 1)
	stepY := maxInt(b.Dy()/8, 1)
	samples := 0
	var minR, minG, minB uint8 = 255, 255, 255
	var maxR, maxG, maxB uint8
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bb, _ := img.At(x, y).RGBA()
			rr, gg, bbb := uint8(r>>8), uint8(g>>8), uint8(bb>>8)
			if rr < minR {
				minR = rr
			}
			if gg < minG {
				minG = gg
			}
			if bbb < minB {
				minB = bbb
			}
			if rr > maxR {
				maxR = rr
			}
			if gg > maxG {
				maxG = gg
			}
			if bbb > maxB {
				maxB = bbb
			}
			samples++
		}
	}
	if samples == 0 {
		return true
	}
	spread := int(maxR-minR) + int(maxG-minG) + int(maxB-minB)
	avg := (int(minR) + int(maxR) + int(minG) + int(maxG) + int(minB) + int(maxB)) / 6
	return spread < 18 && avg >= 185 && avg <= 235
}

func normalizeCardImageURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func drawAvatar(dc *gg.Context, fontBytes []byte, img image.Image, x, y, size int, author string) {
	if img != nil {
		dc.DrawImage(circleCrop(img, size), x, y)
		return
	}
	dc.SetRGB255(220, 238, 255)
	dc.DrawCircle(float64(x+size/2), float64(y+size/2), float64(size/2))
	dc.Fill()
	mustFont(dc, fontBytes, 46)
	dc.SetRGB255(80, 150, 220)
	dc.DrawStringAnchored(firstAvatarLetter(author), float64(x+size/2), float64(y+size/2+5), 0.5, 0.5)
}

func drawHeaderGradient(dc *gg.Context, w, h int) {
	for y := 0; y < h; y++ {
		t := float64(y) / float64(maxInt(h-1, 1))
		alpha := int(122 - t*58)
		dc.SetRGBA255(12, 22, 42, alpha)
		dc.DrawRectangle(0, float64(y), float64(w), 1)
		dc.Fill()
	}
}

func drawGalleryPanel(dc *gg.Context, x, y, w, h int) {
	platePad := 10
	px, py := float64(x-platePad), float64(y-platePad)
	pw, ph := float64(w+platePad*2), float64(h+platePad*2)
	for i := 18; i >= 1; i-- {
		dc.SetRGBA255(64, 104, 145, 3+i*2)
		dc.DrawRoundedRectangle(px, py+float64(i), pw, ph, 30)
		dc.Fill()
	}
	dc.SetRGB255(174, 219, 245)
	dc.DrawRoundedRectangle(px, py, pw, ph, 30)
	dc.Fill()
	for i := 12; i >= 1; i-- {
		dc.SetRGBA255(70, 110, 150, 4+i*2)
		dc.DrawRoundedRectangle(float64(x), float64(y+i), float64(w), float64(h), 24)
		dc.Fill()
	}
	dc.SetRGB255(232, 238, 252)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 24)
	dc.FillPreserve()
	dc.SetLineWidth(4)
	dc.SetRGB255(255, 255, 255)
	dc.Stroke()
}

func drawGlassHeader(dc *gg.Context, fontBytes []byte, platform string, x, y, w, h int) {
	bg := gg.NewContext(w, h)
	p := platformPalette(platform)
	bg.SetColor(softenedColor(p.LogoText, 170))
	bg.Clear()
	if logo := loadPlatformLogo(platform); logo != nil {
		big := imaging.Fill(logo, w, h*2, imaging.Center, imaging.Lanczos)
		bg.DrawImageAnchored(big, w/2, h/2, 0.5, 0.5)
	} else {
		mustFont(bg, fontBytes, 168)
		bg.SetRGBA255(255, 255, 255, 150)
		bg.DrawStringAnchored(platformLogoText(platform), float64(w)/2, float64(h)/2+10, 0.5, 0.5)
	}
	blur := imaging.Blur(bg.Image(), 18)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 24)
	dc.ClipPreserve()
	dc.DrawImage(blur, x, y)
	for yy := 0; yy < h; yy++ {
		t := float64(yy) / float64(maxInt(h-1, 1))
		alpha := int(120 - t*42)
		dc.SetRGBA255(34, 38, 44, alpha)
		dc.DrawRectangle(float64(x), float64(y+yy), float64(w), 1)
		dc.Fill()
	}
	dc.SetRGBA255(255, 255, 255, 54)
	dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
	dc.Fill()
	dc.ResetClip()
}

func softenedColor(c color.RGBA, min int) color.RGBA {
	blend := func(v uint8) uint8 {
		out := int(v)
		if out < min {
			out = min
		}
		return uint8(out)
	}
	return color.RGBA{R: blend(c.R), G: blend(c.G), B: blend(c.B), A: 255}
}

func drawAvatarWithBorder(dc *gg.Context, fontBytes []byte, img image.Image, x, y, size int, author string) {
	border := 3
	cx := float64(x + size/2)
	cy := float64(y + size/2)
	dc.SetRGBA255(255, 255, 255, 245)
	dc.DrawCircle(cx, cy, float64(size/2+border))
	dc.Fill()
	drawAvatar(dc, fontBytes, img, x, y, size, author)
}

func circleCrop(img image.Image, size int) image.Image {
	thumb := imaging.Fill(img, size, size, imaging.Center, imaging.Lanczos)
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size-1) / 2
	r2 := c * c
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx := float64(x) - c
			dy := float64(y) - c
			if dx*dx+dy*dy <= r2 {
				out.Set(x, y, thumb.At(x, y))
			}
		}
	}
	return out
}

func firstAvatarLetter(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return "?"
	}
	rs := []rune(author)
	return string(rs[0])
}

func cardDisplayAuthor(author string) string {
	author = strings.TrimSpace(author)
	if idx := strings.Index(author, "("); idx > 0 {
		return strings.TrimSpace(author[:idx])
	}
	if idx := strings.Index(author, "（"); idx > 0 {
		return strings.TrimSpace(author[:idx])
	}
	if idx := strings.Index(author, " @"); idx > 0 {
		return strings.TrimSpace(author[:idx])
	}
	if idx := strings.Index(author, "@"); idx == 0 {
		return ""
	}
	return author
}

var emojiImageCache sync.Map
var platformLogoCache sync.Map

func renderDefaultPlatformLogoImage(platform string) (image.Image, error) {
	fontBytes, err := file.GetLazyData(text.GlowSansFontFile, control.Md5File, true)
	if err != nil {
		return nil, err
	}
	dc := gg.NewContext(238, 88)
	dc.SetRGB255(255, 255, 255)
	dc.Clear()
	if !drawWhiteLogoBadge(dc, fontBytes, platform, 238, 44) {
		drawPlatformLogo(dc, fontBytes, platform, 214, 44)
	}
	return dc.Image(), nil
}

func drawInlineEmoji(dc *gg.Context, fontBytes []byte, size float64, c color.Color, s string, x, y float64) float64 {
	if s == "" {
		return 0
	}
	mustFont(dc, fontBytes, size)
	dc.SetColor(c)
	cursor := x
	buf := strings.Builder{}
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		part := buf.String()
		dc.DrawStringAnchored(part, cursor, y, 0, 0.5)
		w, _ := dc.MeasureString(part)
		cursor += w
		buf.Reset()
	}
	for _, r := range s {
		if isVariationSelector(r) {
			continue
		}
		if !isEmojiRune(r) {
			buf.WriteRune(r)
			continue
		}
		flush()
		emojiSize := int(size * 1.12)
		if img := fetchEmojiImage(r, emojiSize); img != nil {
			dc.DrawImage(img, int(cursor), int(y-float64(emojiSize)/2))
		} else {
			dc.DrawStringAnchored(string(r), cursor, y, 0, 0.5)
		}
		cursor += float64(emojiSize) + size*0.08
		mustFont(dc, fontBytes, size)
		dc.SetColor(c)
	}
	flush()
	return cursor - x
}

func drawShadowText(dc *gg.Context, fontBytes []byte, size float64, s string, x, y float64) {
	drawInlineEmoji(dc, fontBytes, size, color.RGBA{R: 0, G: 0, B: 0, A: 90}, s, x+2, y+2)
	drawInlineEmoji(dc, fontBytes, size, color.RGBA{R: 255, G: 255, B: 255, A: 255}, s, x, y)
}

func drawTopicLine(dc *gg.Context, fontBytes []byte, size float64, s string, x, y float64) {
	topicRE := regexp.MustCompile(`#([^#\s]+(?:\[话题\])?)#`)
	matches := topicRE.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		drawInlineEmoji(dc, fontBytes, size, color.RGBA{R: 51, G: 51, B: 51, A: 255}, s, x, y)
		return
	}
	cursor := x
	last := 0
	for _, m := range matches {
		if m[0] > last {
			cursor += drawInlineEmoji(dc, fontBytes, size, color.RGBA{R: 51, G: 51, B: 51, A: 255}, s[last:m[0]], cursor, y)
		}
		cursor += drawInlineEmoji(dc, fontBytes, size, color.RGBA{R: 90, G: 134, B: 206, A: 255}, s[m[0]:m[1]], cursor, y)
		last = m[1]
	}
	if last < len(s) {
		drawInlineEmoji(dc, fontBytes, size, color.RGBA{R: 51, G: 51, B: 51, A: 255}, s[last:], cursor, y)
	}
}

func isVariationSelector(r rune) bool {
	return r == 0xfe0e || r == 0xfe0f
}

func isEmojiRune(r rune) bool {
	return (r >= 0x1f300 && r <= 0x1faff) || (r >= 0x2600 && r <= 0x27bf)
}

func fetchEmojiImage(r rune, size int) image.Image {
	key := strconv.FormatInt(int64(r), 16)
	if v, ok := emojiImageCache.Load(key); ok {
		if img, _ := v.(image.Image); img != nil {
			return imaging.Fit(img, size, size, imaging.Lanczos)
		}
		return nil
	}
	raw := "https://cdn.jsdelivr.net/gh/twitter/twemoji@14.0.2/assets/72x72/" + key + ".png"
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		emojiImageCache.Store(key, image.Image(nil))
		return nil
	}
	req.Header.Set("User-Agent", defaultUA)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		emojiImageCache.Store(key, image.Image(nil))
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		emojiImageCache.Store(key, image.Image(nil))
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		emojiImageCache.Store(key, image.Image(nil))
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		emojiImageCache.Store(key, image.Image(nil))
		return nil
	}
	emojiImageCache.Store(key, img)
	return imaging.Fit(img, size, size, imaging.Lanczos)
}

func drawPlatformLogo(dc *gg.Context, fontBytes []byte, platform string, right, cy float64) {
	if platform == "twitter" {
		drawTwitterTransparentLogo(dc, right, cy)
		return
	}
	if drawCustomPlatformLogoBadge(dc, platform, right, cy) {
		return
	}
	if platform == "youtube" {
		drawYouTubeTransparentLogo(dc, fontBytes, right, cy)
		return
	}
	if platform == "instagram" {
		drawInstagramTransparentLogo(dc, fontBytes, right, cy)
		return
	}
	if drawWhiteLogoBadge(dc, fontBytes, platform, right, cy) {
		return
	}
	switch platform {
	case "douyin", "tiktok":
		drawDouyinMark(dc, right-150, cy-44)
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(22, 22, 26)
		name := "抖音"
		if platform == "tiktok" {
			name = "TikTok"
		}
		dc.DrawStringAnchored(name, right, cy+2, 1, 0.5)
	case "xiaohongshu":
		mustFont(dc, fontBytes, 40)
		dc.SetRGB255(255, 36, 66)
		dc.DrawStringAnchored("小红书", right, cy, 1, 0.5)
	case "weibo":
		drawWeiboMark(dc, right-158, cy-35)
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(230, 65, 54)
		dc.DrawStringAnchored("微博", right, cy+2, 1, 0.5)
	case "kuaishou":
		drawKuaishouMark(dc, right-150, cy-32)
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(255, 86, 0)
		dc.DrawStringAnchored("快手", right, cy+2, 1, 0.5)
	case "xianyu":
		drawXianyuMark(dc, right-150, cy-32)
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(39, 176, 154)
		dc.DrawStringAnchored("闲鱼", right, cy+2, 1, 0.5)
	case "toutiao":
		mustFont(dc, fontBytes, 38)
		dc.SetRGB255(239, 35, 42)
		dc.DrawStringAnchored("头条", right, cy, 1, 0.5)
	case "xiaoheihe":
		drawXiaoheiheMark(dc, right-190, cy-32)
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(20, 25, 30)
		dc.DrawStringAnchored("小黑盒", right, cy+2, 1, 0.5)
	default:
		name := platformLogoText(platform)
		p := platformPalette(platform)
		mustFont(dc, fontBytes, 38)
		dc.SetColor(p.LogoText)
		dc.DrawStringAnchored(name, right, cy, 1, 0.5)
	}
}

func drawTwitterTransparentLogo(dc *gg.Context, right, cy float64) {
	x := right - 60
	y := cy - 30
	dc.SetRGBA255(0, 0, 0, 80)
	dc.SetLineWidth(9)
	dc.DrawLine(x+11, y+8, x+51, y+52)
	dc.DrawLine(x+49, y+8, x+9, y+52)
	dc.Stroke()
	dc.SetRGB255(255, 255, 255)
	dc.SetLineWidth(7)
	dc.DrawLine(x+11, y+8, x+51, y+52)
	dc.DrawLine(x+49, y+8, x+9, y+52)
	dc.Stroke()
}

func drawTwitterLogoBadge(dc *gg.Context, right, cy float64) bool {
	const size = 74.0
	x, y := right-size, cy-size/2
	dc.SetRGB255(5, 5, 5)
	dc.DrawRoundedRectangle(x, y, size, size, 9)
	dc.Fill()
	dc.SetRGBA255(255, 255, 255, 210)
	dc.SetLineWidth(2)
	dc.DrawRoundedRectangle(x+1, y+1, size-2, size-2, 8)
	dc.Stroke()
	dc.SetRGB255(255, 255, 255)
	dc.SetLineWidth(6)
	dc.DrawLine(x+22, y+19, x+53, y+55)
	dc.DrawLine(x+52, y+19, x+21, y+55)
	dc.Stroke()
	return true
}

func drawYouTubeTransparentLogo(dc *gg.Context, fontBytes []byte, right, cy float64) {
	iconW, iconH := 82.0, 58.0
	x := right - 230
	y := cy - iconH/2
	dc.SetRGB255(255, 0, 0)
	dc.DrawRoundedRectangle(x, y, iconW, iconH, 14)
	dc.Fill()
	dc.SetRGB255(255, 255, 255)
	dc.DrawRegularPolygon(3, x+47, cy, 19, gg.Radians(90))
	dc.Fill()
	mustFont(dc, fontBytes, 38)
	dc.SetRGBA255(255, 255, 255, 90)
	dc.DrawStringAnchored("YouTube", right+2, cy+3, 1, 0.5)
	dc.SetRGB255(255, 255, 255)
	dc.DrawStringAnchored("YouTube", right, cy+1, 1, 0.5)
}

func drawInstagramTransparentLogo(dc *gg.Context, fontBytes []byte, right, cy float64) {
	x := right - 236
	y := cy - 36
	w, h := 66.0, 66.0
	dc.SetLineWidth(7)
	dc.SetRGB255(245, 96, 64)
	dc.DrawRoundedRectangle(x, y, w, h, 18)
	dc.Stroke()
	dc.SetLineWidth(6)
	dc.SetRGB255(193, 53, 132)
	dc.DrawCircle(x+w/2, y+h/2, 15)
	dc.Stroke()
	dc.SetRGB255(253, 200, 74)
	dc.DrawCircle(x+w-17, y+17, 5)
	dc.Fill()
	mustFont(dc, fontBytes, 38)
	dc.SetRGBA255(255, 255, 255, 85)
	dc.DrawStringAnchored("Instagram", right+2, cy+3, 1, 0.5)
	dc.SetRGB255(255, 255, 255)
	dc.DrawStringAnchored("Instagram", right, cy+1, 1, 0.5)
}

func drawCustomPlatformLogoBadge(dc *gg.Context, platform string, right, cy float64) bool {
	img := loadPlatformLogo(platform)
	if img == nil {
		return false
	}
	const pad = 8
	bounds := img.Bounds()
	aspect := 1.0
	if bounds.Dx() > 0 && bounds.Dy() > 0 {
		aspect = float64(bounds.Dx()) / float64(bounds.Dy())
	}
	w, h := 238.0, 88.0
	if aspect <= 1.35 {
		w, h = 112.0, 112.0
	}
	x, y := right-w, cy-h/2
	fit := imaging.Fit(img, int(w)-pad*2, int(h)-pad*2, imaging.Lanczos)
	b := fit.Bounds()
	if !logoHasTransparency(img) {
		dc.SetRGB255(255, 255, 255)
		dc.DrawRoundedRectangle(x, y, w, h, 8)
		dc.Fill()
	}
	dc.DrawImage(fit, int(x)+(int(w)-b.Dx())/2, int(y)+(int(h)-b.Dy())/2)
	return true
}

func loadPlatformLogo(platform string) image.Image {
	platform = strings.TrimSpace(strings.ToLower(platform))
	if platform == "" {
		return nil
	}
	if v, ok := platformLogoCache.Load(platform); ok {
		img, _ := v.(image.Image)
		return img
	}
	for _, path := range platformLogoPaths(platform) {
		img := decodeLogoFile(path)
		if img != nil {
			platformLogoCache.Store(platform, img)
			return img
		}
	}
	return nil
}

func platformLogoPaths(platform string) []string {
	names := []string{platform}
	switch platform {
	case "douyin":
		names = append(names, "tiktok")
	case "tiktok":
		names = append(names, "douyin")
	case "twitter":
		names = append(names, "x")
	case "xiaohongshu":
		names = append(names, "xhs")
	case "xiaoheihe":
		names = append(names, "heybox")
	}
	exts := []string{".png", ".jpg", ".jpeg", ".webp"}
	dirs := []string{
		filepath.Join(engine.DataFolder(), "logos"),
		filepath.Join("data", "mediaparser", "logos"),
		filepath.Join("plugin", "mediaparser", "data", "mediaparser", "logos"),
	}
	out := make([]string, 0, len(names)*len(exts)*len(dirs))
	for _, dir := range dirs {
		for _, name := range names {
			for _, ext := range exts {
				out = append(out, filepath.Join(dir, name+ext))
			}
		}
	}
	return out
}

func decodeLogoFile(path string) image.Image {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil
	}
	if !logoHasVisibleContent(img) {
		return nil
	}
	return img
}

func logoHasVisibleContent(img image.Image) bool {
	if img == nil {
		return false
	}
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if total <= 0 {
		return false
	}
	step := total / 1600
	if step < 1 {
		step = 1
	}
	visible := 0
	sampled := 0
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if i%step != 0 {
				i++
				continue
			}
			i++
			sampled++
			r, g, bl, a := img.At(x, y).RGBA()
			if a>>8 < 24 {
				continue
			}
			rr, gg, bb := int(r>>8), int(g>>8), int(bl>>8)
			if rr < 242 || gg < 242 || bb < 242 {
				visible++
			}
		}
	}
	if sampled == 0 {
		return false
	}
	return float64(visible)/float64(sampled) > 0.001
}

func logoHasTransparency(img image.Image) bool {
	if img == nil {
		return false
	}
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if total <= 0 {
		return false
	}
	step := total / 1600
	if step < 1 {
		step = 1
	}
	i := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if i%step != 0 {
				i++
				continue
			}
			i++
			_, _, _, a := img.At(x, y).RGBA()
			if a>>8 < 250 {
				return true
			}
		}
	}
	return false
}

func drawWhiteLogoBadge(dc *gg.Context, fontBytes []byte, platform string, right, cy float64) bool {
	w, h := 238.0, 88.0
	x, y := right-w, cy-h/2
	dc.SetRGB255(255, 255, 255)
	dc.DrawRoundedRectangle(x, y, w, h, 8)
	dc.Fill()
	switch platform {
	case "xiaohongshu":
		mustFont(dc, fontBytes, 46)
		dc.SetRGB255(255, 36, 66)
		dc.DrawStringAnchored("小红书", right-4, cy+2, 1, 0.5)
	case "xiaoheihe":
		drawHeyboxIcon(dc, x+10, y+17)
		mustFont(dc, fontBytes, 30)
		dc.SetRGB255(28, 28, 32)
		dc.DrawStringAnchored("小黑盒", right-4, cy-10, 1, 0.5)
		mustFont(dc, fontBytes, 15)
		dc.DrawStringAnchored("HEYBOX", right-8, cy+24, 1, 0.5)
	case "bilibili":
		mustFont(dc, fontBytes, 40)
		dc.SetRGB255(35, 174, 229)
		dc.DrawStringAnchored("bilibili", right-4, cy+2, 1, 0.5)
	case "twitter":
		mustFont(dc, fontBytes, 68)
		dc.SetRGB255(5, 5, 5)
		dc.DrawStringAnchored("𝕏", right-10, cy+1, 1, 0.5)
	case "kuaishou":
		drawKuaishouMark(dc, x+8, y+10)
		mustFont(dc, fontBytes, 40)
		dc.SetRGB255(64, 64, 64)
		dc.DrawStringAnchored("快手", right-4, cy+2, 1, 0.5)
	case "acfun":
		mustFont(dc, fontBytes, 42)
		dc.SetRGB255(253, 76, 93)
		dc.DrawStringAnchored("AcFun", right-4, cy+2, 1, 0.5)
	case "youtube":
		dc.SetRGB255(255, 0, 0)
		dc.DrawRoundedRectangle(x+20, y+24, 62, 40, 8)
		dc.Fill()
		dc.SetRGB255(255, 255, 255)
		dc.DrawRegularPolygon(3, x+54, y+44, 14, gg.Radians(90))
		dc.Fill()
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(33, 33, 33)
		dc.DrawStringAnchored("YouTube", right-4, cy+2, 1, 0.5)
	case "instagram":
		mustFont(dc, fontBytes, 34)
		dc.SetRGB255(193, 53, 132)
		dc.DrawStringAnchored("Instagram", right-4, cy+2, 1, 0.5)
	default:
		return false
	}
	return true
}

func drawHeyboxIcon(dc *gg.Context, x, y float64) {
	dc.SetRGB255(28, 28, 32)
	dc.MoveTo(x+2, y+28)
	dc.LineTo(x+26, y+14)
	dc.LineTo(x+26, y+28)
	dc.LineTo(x+42, y+36)
	dc.LineTo(x+42, y+52)
	dc.LineTo(x+16, y+38)
	dc.ClosePath()
	dc.Fill()
	dc.MoveTo(x+78, y+28)
	dc.LineTo(x+54, y+14)
	dc.LineTo(x+54, y+28)
	dc.LineTo(x+38, y+36)
	dc.LineTo(x+38, y+52)
	dc.LineTo(x+64, y+38)
	dc.ClosePath()
	dc.Fill()
}

func platformLogoText(platform string) string {
	switch platform {
	case "bilibili":
		return "bilibili"
	case "twitter":
		return "X"
	case "acfun":
		return "AcFun"
	case "youtube":
		return "YouTube"
	case "instagram":
		return "Instagram"
	default:
		return strings.ToUpper(firstNonEmpty(platform, "media"))
	}
}

func platformPalette(platform string) cardPalette {
	switch platform {
	case "bilibili":
		return cardPalette{LogoText: color.RGBA{R: 0, G: 174, B: 236, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "douyin", "tiktok":
		return cardPalette{LogoText: color.RGBA{R: 20, G: 20, B: 24, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "xiaohongshu":
		return cardPalette{LogoText: color.RGBA{R: 255, G: 36, B: 66, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "weibo":
		return cardPalette{LogoText: color.RGBA{R: 230, G: 65, B: 54, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "kuaishou":
		return cardPalette{LogoText: color.RGBA{R: 255, G: 86, B: 0, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "acfun":
		return cardPalette{LogoText: color.RGBA{R: 253, G: 76, B: 93, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "youtube":
		return cardPalette{LogoText: color.RGBA{R: 255, G: 0, B: 0, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	case "instagram":
		return cardPalette{LogoText: color.RGBA{R: 193, G: 53, B: 132, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	default:
		return cardPalette{LogoText: color.RGBA{R: 70, G: 90, B: 120, A: 255}, Title: color.RGBA{R: 109, G: 49, B: 159, A: 255}}
	}
}

func drawCover(dc *gg.Context, img image.Image, x, y, w, h int, showPlay bool) {
	if img != nil {
		cover := imaging.Fill(img, w, h, imaging.Center, imaging.Lanczos)
		dc.DrawImage(cover, x, y)
	} else {
		dc.SetRGB255(238, 238, 238)
		dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
		dc.Fill()
	}
	if !showPlay {
		return
	}
	cx := float64(x + w/2)
	cy := float64(y + h/2)
	dc.SetRGBA255(80, 80, 80, 105)
	dc.DrawCircle(cx, cy, 70)
	dc.Fill()
	dc.SetRGBA255(255, 255, 255, 165)
	dc.DrawRegularPolygon(3, cx+14, cy, 54, gg.Radians(90))
	dc.Fill()
}

func drawFloatingCoverCell(dc *gg.Context, img image.Image, x, y, w, h int, showPlay bool) {
	drawFloatingImageCellAnchored(dc, img, x, y, w, h, imaging.Center)
	if !showPlay {
		return
	}
	cx := float64(x + w/2)
	cy := float64(y + h/2)
	dc.SetRGBA255(42, 42, 42, 118)
	dc.DrawCircle(cx, cy, 70)
	dc.Fill()
	dc.SetRGBA255(255, 255, 255, 190)
	dc.DrawRegularPolygon(3, cx+14, cy, 54, gg.Radians(90))
	dc.Fill()
}

func shouldDrawPlayOverlay(meta mediaMeta) bool {
	return len(meta.VideoURLs) > 0 && !(isCombinedMediaPlatform(meta.Platform) && hasMixedMediaItems(meta))
}

func galleryGridHeightForImages(imgs []image.Image, w int) int {
	if len(imgs) == 0 {
		return 0
	}
	gap := 10
	if len(imgs) == 1 {
		return w
	}
	if useThreeImageMosaic(imgs) {
		return (w - gap) / 2
	}
	cols := galleryGridCols(len(imgs))
	colW := (w - gap*(cols-1)) / cols
	limit := len(imgs)
	if limit > 9 {
		limit = 9
	}
	rows := (limit + cols - 1) / cols
	return rows*colW + gap*(rows-1)
}

func drawGalleryGrid(dc *gg.Context, imgs []image.Image, x, y, w, h int) {
	if len(imgs) == 0 {
		return
	}
	gap := 10
	if len(imgs) == 1 {
		drawFloatingImageCell(dc, imgs[0], x, y, w, h)
		return
	}
	if useThreeImageMosaic(imgs) {
		drawThreeImageMosaic(dc, imgs, x, y, w)
		return
	}
	cols := galleryGridCols(len(imgs))
	cellW := (w - gap*(cols-1)) / cols
	limit := len(imgs)
	if limit > 9 {
		limit = 9
	}
	for i := 0; i < limit; i++ {
		row := i / cols
		col := i % cols
		drawFloatingImageCell(dc, imgs[i], x+col*(cellW+gap), y+row*(cellW+gap), cellW, cellW)
	}
}

func useThreeImageMosaic(imgs []image.Image) bool {
	if len(imgs) != 3 {
		return false
	}
	landscape := 0
	for _, img := range imgs {
		if img == nil {
			continue
		}
		b := img.Bounds()
		if b.Dx() <= 0 || b.Dy() <= 0 {
			continue
		}
		if float64(b.Dx())/float64(b.Dy()) >= 1.1 {
			landscape++
		}
	}
	return landscape >= 2
}

func drawThreeImageMosaic(dc *gg.Context, imgs []image.Image, x, y, w int) {
	gap := 10
	totalH := (w - gap) / 2
	leftW := (w - gap) / 2
	rightW := w - leftW - gap
	rightH := (totalH - gap) / 2
	drawFloatingImageCellAnchored(dc, imgs[0], x, y, leftW, totalH, imaging.Center)
	drawFloatingImageCellAnchored(dc, imgs[1], x+leftW+gap, y, rightW, rightH, imaging.Center)
	drawFloatingImageCellAnchored(dc, imgs[2], x+leftW+gap, y+rightH+gap, rightW, rightH, imaging.Center)
}

func galleryGridCols(n int) int {
	switch {
	case n <= 1:
		return 1
	case n == 2 || n == 4:
		return 2
	default:
		return 3
	}
}

func drawImageCell(dc *gg.Context, img image.Image, x, y, w, h int, preserve bool) {
	if img == nil {
		dc.SetRGB255(238, 238, 238)
		dc.DrawRectangle(float64(x), float64(y), float64(w), float64(h))
		dc.Fill()
		return
	}
	if preserve {
		dc.DrawImage(imaging.Resize(img, w, h, imaging.Lanczos), x, y)
		return
	}
	dc.DrawImage(imaging.Fill(img, w, h, imaging.Top, imaging.Lanczos), x, y)
}

func drawFloatingImageCell(dc *gg.Context, img image.Image, x, y, w, h int) {
	drawFloatingImageCellAnchored(dc, img, x, y, w, h, imaging.Top)
}

func drawFloatingImageCellAnchored(dc *gg.Context, img image.Image, x, y, w, h int, anchor imaging.Anchor) {
	const (
		radius = 12.0
		border = 3.0
	)
	fx, fy := float64(x), float64(y)
	fw, fh := float64(w), float64(h)
	for i := 8; i >= 1; i-- {
		alpha := 8 + i*4
		dc.SetRGBA255(0, 0, 0, alpha)
		dc.DrawRoundedRectangle(fx, fy+float64(i), fw, fh, radius)
		dc.Fill()
	}
	dc.SetRGB255(255, 255, 255)
	dc.DrawRoundedRectangle(fx, fy, fw, fh, radius)
	dc.Fill()

	ix := x + int(border)
	iy := y + int(border)
	iw := w - int(border)*2
	ih := h - int(border)*2
	dc.DrawRoundedRectangle(float64(ix), float64(iy), float64(iw), float64(ih), radius-border)
	dc.ClipPreserve()
	if img == nil {
		dc.SetRGB255(238, 238, 238)
		dc.Fill()
	} else {
		dc.DrawImage(imaging.Fill(img, iw, ih, anchor, imaging.Lanczos), ix, iy)
	}
	dc.ResetClip()
}

func drawFloatingImageCellContain(dc *gg.Context, img image.Image, x, y, w, h int) {
	const (
		radius = 12.0
		border = 3.0
	)
	fx, fy := float64(x), float64(y)
	fw, fh := float64(w), float64(h)
	for i := 8; i >= 1; i-- {
		alpha := 8 + i*4
		dc.SetRGBA255(0, 0, 0, alpha)
		dc.DrawRoundedRectangle(fx, fy+float64(i), fw, fh, radius)
		dc.Fill()
	}
	dc.SetRGB255(255, 255, 255)
	dc.DrawRoundedRectangle(fx, fy, fw, fh, radius)
	dc.Fill()

	ix := x + int(border)
	iy := y + int(border)
	iw := w - int(border)*2
	ih := h - int(border)*2
	dc.DrawRoundedRectangle(float64(ix), float64(iy), float64(iw), float64(ih), radius-border)
	dc.ClipPreserve()
	dc.SetRGB255(255, 255, 255)
	dc.Fill()
	if img != nil {
		fit := imaging.Fit(img, iw, ih, imaging.Lanczos)
		b := fit.Bounds()
		dc.DrawImage(fit, ix+(iw-b.Dx())/2, iy+(ih-b.Dy())/2)
	}
	dc.ResetClip()
}

func longImageCardHeight(img image.Image, w int) int {
	if img == nil {
		return w
	}
	h := scaledImageHeight(img, w)
	if h < 420 {
		return 420
	}
	if h > 2100 {
		return 2100
	}
	return h
}

func scaledImageHeight(img image.Image, w int) int {
	if img == nil {
		return w
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return w
	}
	return int(float64(w) * float64(b.Dy()) / float64(b.Dx()))
}

func shortestColumn(heights []int) int {
	idx := 0
	for i := 1; i < len(heights); i++ {
		if heights[i] < heights[idx] {
			idx = i
		}
	}
	return idx
}

func shortestColumnOffset(offsets []int, base int) int {
	idx := 0
	for i := 1; i < len(offsets); i++ {
		if offsets[i]-base < offsets[idx]-base {
			idx = i
		}
	}
	return idx
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func maxInt(values ...int) int {
	out := 0
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	return out
}

func drawDouyinMark(dc *gg.Context, x, y float64) {
	drawMusicNote(dc, x+6, y+4, color.RGBA{R: 37, G: 244, B: 238, A: 255})
	drawMusicNote(dc, x-4, y+10, color.RGBA{R: 255, G: 0, B: 80, A: 255})
	drawMusicNote(dc, x, y+7, color.RGBA{R: 18, G: 18, B: 22, A: 255})
}

func drawMusicNote(dc *gg.Context, x, y float64, c color.Color) {
	dc.SetColor(c)
	dc.SetLineWidth(13)
	dc.DrawLine(x+42, y+6, x+42, y+62)
	dc.Stroke()
	dc.DrawCircle(x+24, y+63, 19)
	dc.Fill()
	dc.DrawRectangle(x+42, y+6, 35, 13)
	dc.Fill()
}

func drawWeiboMark(dc *gg.Context, x, y float64) {
	dc.SetRGB255(230, 65, 54)
	dc.DrawEllipse(x+36, y+36, 38, 26)
	dc.Fill()
	dc.SetRGB255(255, 185, 40)
	dc.DrawCircle(x+48, y+25, 13)
	dc.Fill()
	dc.SetRGB255(255, 255, 255)
	dc.DrawEllipse(x+36, y+38, 22, 14)
	dc.Fill()
	dc.SetRGB255(20, 25, 30)
	dc.DrawCircle(x+30, y+36, 5)
	dc.DrawCircle(x+45, y+36, 5)
	dc.Fill()
}

func drawKuaishouMark(dc *gg.Context, x, y float64) {
	dc.SetRGB255(255, 86, 0)
	dc.DrawRoundedRectangle(x+10, y+14, 58, 48, 10)
	dc.Fill()
	dc.SetRGB255(255, 255, 255)
	dc.DrawCircle(x+29, y+38, 8)
	dc.DrawCircle(x+49, y+38, 8)
	dc.Fill()
}

func drawXianyuMark(dc *gg.Context, x, y float64) {
	dc.SetRGB255(255, 219, 65)
	dc.DrawEllipse(x+38, y+36, 34, 23)
	dc.Fill()
	dc.SetRGB255(39, 176, 154)
	dc.MoveTo(x+68, y+36)
	dc.LineTo(x+88, y+20)
	dc.LineTo(x+88, y+52)
	dc.ClosePath()
	dc.Fill()
	dc.SetRGB255(20, 25, 30)
	dc.DrawCircle(x+25, y+31, 4)
	dc.Fill()
}

func drawXiaoheiheMark(dc *gg.Context, x, y float64) {
	dc.SetRGB255(20, 25, 30)
	dc.DrawRoundedRectangle(x+8, y+8, 56, 56, 12)
	dc.Fill()
	dc.SetRGB255(255, 255, 255)
	dc.DrawRectangle(x+22, y+26, 28, 20)
	dc.Fill()
	dc.SetRGB255(20, 25, 30)
	dc.DrawRectangle(x+28, y+31, 5, 5)
	dc.DrawRectangle(x+40, y+31, 5, 5)
	dc.Fill()
}

func wrapDisplayText(s string, width, maxLines int) []string {
	out := wrapCardText(strings.TrimSpace(s), width)
	if maxLines > 0 && len(out) > maxLines {
		out = out[:maxLines]
		rs := []rune(out[len(out)-1])
		if len(rs) > 1 {
			out[len(out)-1] = strings.TrimRight(string(rs[:len(rs)-1]), "锛屻€?. ") + "..."
		}
	}
	return out
}

func wrapDisplayTextByPixels(fontBytes []byte, size float64, s string, maxW float64, maxLines int) []string {
	out := wrapTextByPixels(gg.NewContext(cardWidth, 100), fontBytes, size, strings.TrimSpace(s), maxW)
	if maxLines > 0 && len(out) > maxLines {
		out = out[:maxLines]
		rs := []rune(out[len(out)-1])
		if len(rs) > 1 {
			out[len(out)-1] = strings.TrimRight(string(rs[:len(rs)-1]), "，。！？；：、,.!?;: ") + "..."
		}
	}
	return fixWrappedLinePunctuation(out)
}

func wrapCardText(s string, width int) []string {
	out := []string{}
	for _, raw := range strings.Split(s, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		out = append(out, wrapCardParagraph(raw, width)...)
	}
	return out
}

func wrapCardParagraph(s string, width int) []string {
	tokens := cardWrapTokens(s)
	lines := []string{}
	line := ""
	lineW := 0
	for _, token := range tokens {
		tokenW := cardTextWidth(token)
		if tokenW == 0 {
			continue
		}
		if line != "" && lineW+tokenW > width {
			lines = append(lines, strings.TrimSpace(line))
			line, lineW = "", 0
		}
		if line == "" && tokenW > width {
			chunk := ""
			chunkW := 0
			for _, r := range token {
				rw := cardRuneWidth(r)
				if chunk != "" && chunkW+rw > width {
					lines = append(lines, strings.TrimSpace(chunk))
					chunk, chunkW = "", 0
				}
				chunk += string(r)
				chunkW += rw
			}
			line, lineW = chunk, chunkW
			continue
		}
		line += token
		lineW += tokenW
	}
	if strings.TrimSpace(line) != "" {
		lines = append(lines, strings.TrimSpace(line))
	}
	return fixWrappedLinePunctuation(lines)
}

func cardWrapTokens(s string) []string {
	tokens := []string{}
	buf := strings.Builder{}
	flush := func() {
		if buf.Len() > 0 {
			tokens = append(tokens, buf.String())
			buf.Reset()
		}
	}
	for _, r := range s {
		if r == '\t' || r == '\r' || r == '\n' {
			r = ' '
		}
		if r == ' ' {
			flush()
			if len(tokens) > 0 && tokens[len(tokens)-1] != " " {
				tokens = append(tokens, " ")
			}
			continue
		}
		if cardRuneWidth(r) >= 2 {
			flush()
			tokens = append(tokens, string(r))
			continue
		}
		buf.WriteRune(r)
	}
	flush()
	return tokens
}

func cardTextWidth(s string) int {
	w := 0
	for _, r := range s {
		w += cardRuneWidth(r)
	}
	return w
}

func cardRuneWidth(r rune) int {
	if r == ' ' {
		return 1
	}
	if isEmojiRune(r) {
		return 2
	}
	return 1
}

func fixWrappedLinePunctuation(lines []string) []string {
	if len(lines) < 2 {
		return lines
	}
	out := append([]string(nil), lines...)
	for i := 0; i < len(out)-1; i++ {
		out[i] = strings.TrimSpace(out[i])
		out[i+1] = strings.TrimSpace(out[i+1])
		if out[i] == "" || out[i+1] == "" {
			continue
		}
		nextRunes := []rune(out[i+1])
		if len(nextRunes) > 0 && isNoLineStartRune(nextRunes[0]) {
			out[i] += string(nextRunes[0])
			out[i+1] = strings.TrimSpace(string(nextRunes[1:]))
			continue
		}
		lineRunes := []rune(out[i])
		if len(lineRunes) > 0 && isNoLineEndRune(lineRunes[len(lineRunes)-1]) {
			out[i+1] = string(lineRunes[len(lineRunes)-1]) + out[i+1]
			out[i] = strings.TrimSpace(string(lineRunes[:len(lineRunes)-1]))
		}
	}
	filtered := out[:0]
	for _, line := range out {
		if strings.TrimSpace(line) != "" {
			filtered = append(filtered, strings.TrimSpace(line))
		}
	}
	return filtered
}

func isNoLineStartRune(r rune) bool {
	return strings.ContainsRune("，。！？；：、）】》」』”’〉》〕］｝…～,.!?;:%)]}", r)
}

func isNoLineEndRune(r rune) bool {
	return strings.ContainsRune("（【《「『“‘〈《〔［｛([{", r)
}
