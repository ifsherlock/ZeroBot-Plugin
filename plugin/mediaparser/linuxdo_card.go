package mediaparser

import (
	"image"
	"image/color"
	"strings"
	"sync"

	"github.com/FloatTech/gg"
	"github.com/disintegration/imaging"
)

const (
	linuxdoCardWidth           = 760
	linuxdoCardOuterPad        = 40
	linuxdoCardPanelPad        = 28
	linuxdoCardContentXPad     = 8
	linuxdoCardContentBoxInset = 12
	linuxdoCardMaxImageHeight  = 960
	linuxdoCardMaxHeight       = 28000
)

type linuxdoRenderBlock struct {
	kind   string
	text   string
	url    string
	lines  []string
	img    image.Image
	width  int
	height int
}

func renderLinuxdoShareCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	theme := keylolCardThemeNow()
	bg := theme.BG
	panel := theme.Panel
	contentBG := linuxdoContentBoxColor(theme)
	titleColor := theme.Title
	mutedColor := theme.Muted
	lineColor := theme.Line

	contentW := linuxdoCardWidth - linuxdoCardOuterPad*2 - linuxdoCardPanelPad*2 - linuxdoCardContentXPad*2
	boxW := contentW - linuxdoCardContentBoxInset*2
	titleLines := wrapDisplayTextByPixels(fontBytes, 30, firstNonEmpty(meta.Title, "Linux.do"), float64(contentW), 4)
	blocks := linuxdoPrepareRenderBlocks(meta, bodyFontBytes, boxW-36)
	headerH := 38 + 58 + len(titleLines)*42 + 16 + 76 + 24
	footerH := 30 + 36 + 44
	maxContentBoxH := linuxdoCardMaxHeight - linuxdoCardOuterPad*2 - headerH - footerH
	blocks = linuxdoConstrainRenderBlocks(blocks, maxContentBoxH-52)
	contentH := linuxdoRenderBlocksHeight(blocks)
	contentBoxH := maxInt(118, contentH+52)
	panelH := headerH + contentBoxH + footerH
	h := linuxdoCardOuterPad*2 + panelH

	dc := gg.NewContext(linuxdoCardWidth, h)
	setRGB(dc, bg)
	dc.Clear()
	for i := 12; i >= 1; i-- {
		dc.SetRGBA255(130, 96, 46, 3+i)
		dc.DrawRoundedRectangle(float64(linuxdoCardOuterPad), float64(linuxdoCardOuterPad+i), float64(linuxdoCardWidth-linuxdoCardOuterPad*2), float64(panelH), 18)
		dc.Fill()
	}
	setRGB(dc, panel)
	dc.DrawRoundedRectangle(float64(linuxdoCardOuterPad), float64(linuxdoCardOuterPad), float64(linuxdoCardWidth-linuxdoCardOuterPad*2), float64(panelH), 18)
	dc.Fill()

	x := linuxdoCardOuterPad + linuxdoCardPanelPad + linuxdoCardContentXPad
	y := linuxdoCardOuterPad + 38
	drawLinuxdoLogo(dc, fontBytes, x, y, titleColor)
	y += 58
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, 30, titleColor, line, float64(x), float64(y))
		y += 42
	}
	y += 16

	avatar := fetchCardImage(meta.Avatar, meta.ImageHeads)
	drawAvatar(dc, fontBytes, avatar, x, y-4, 54, meta.Author)
	drawInlineEmoji(dc, fontBytes, 23, titleColor, firstNonEmpty(meta.Author, "Linux.do 用户"), float64(x+72), float64(y+18))
	sub := strings.TrimSpace(meta.Timestamp)
	if sub != "" {
		sub = "@" + strings.TrimSpace(meta.Author) + " · " + sub
	} else if meta.Author != "" {
		sub = "@" + strings.TrimSpace(meta.Author)
	}
	if sub != "" {
		drawInlineEmoji(dc, fontBytes, 18, mutedColor, sub, float64(x+72), float64(y+48))
	}
	y += 76
	setRGB(dc, lineColor)
	dc.DrawRectangle(float64(x), float64(y), float64(contentW), 1)
	dc.Fill()
	y += 24

	boxX := x + linuxdoCardContentBoxInset
	setRGB(dc, contentBG)
	dc.DrawRoundedRectangle(float64(boxX), float64(y), float64(boxW), float64(contentBoxH), 12)
	dc.Fill()
	blockY := y + 26
	for i, block := range blocks {
		if i > 0 {
			blockY += linuxdoRenderBlockGap(blocks[i-1], block)
		}
		linuxdoDrawRenderBlock(dc, bodyFontBytes, fontBytes, block, boxX+18, blockY, boxW-36, theme)
		blockY += block.height
	}
	y += contentBoxH + 30

	setRGB(dc, lineColor)
	dc.DrawRectangle(float64(x), float64(y), float64(contentW), 1)
	dc.Fill()
	y += 36
	footer := firstNonEmpty(meta.URL, meta.SourceURL, linuxdoReferer)
	footer = truncateTextByPixels(fontBytes, 18, "🔗 "+footer, float64(contentW))
	drawInlineEmoji(dc, fontBytes, 18, mutedColor, footer, float64(x), float64(y))
	return saveCardPNG(dc, meta)
}

func linuxdoPrepareRenderBlocks(meta mediaMeta, fontBytes []byte, contentW int) []linuxdoRenderBlock {
	blocks := linuxdoBlocksForRender(meta)
	images := make([]image.Image, len(blocks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, block := range blocks {
		if block.Kind != "image" {
			continue
		}
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			images[i] = fetchCardImage(raw, meta.ImageHeads)
		}(i, block.URL)
	}
	wg.Wait()

	measure := gg.NewContext(linuxdoCardWidth, 100)
	renderBlocks := make([]linuxdoRenderBlock, 0, len(blocks))
	for i, block := range blocks {
		if block.Kind == "image" {
			width, height := linuxdoImageDisplaySize(images[i], contentW, linuxdoCardMaxImageHeight)
			renderBlocks = append(renderBlocks, linuxdoRenderBlock{
				kind: "image", url: block.URL, img: images[i], width: width, height: height,
			})
			continue
		}
		size, lineH, innerW, extraH := linuxdoTextBlockMetrics(block.Kind, contentW)
		lines := wrapTextByPixels(measure, fontBytes, size, block.Text, float64(innerW))
		if len(lines) == 0 {
			continue
		}
		renderBlocks = append(renderBlocks, linuxdoRenderBlock{
			kind: block.Kind, text: block.Text, url: block.URL, lines: lines, width: contentW, height: len(lines)*lineH + extraH,
		})
	}
	if len(renderBlocks) == 0 {
		lines := wrapTextByPixels(measure, fontBytes, 24, "暂无正文摘要", float64(contentW))
		return []linuxdoRenderBlock{{kind: "text", text: "暂无正文摘要", lines: lines, width: contentW, height: len(lines) * 34}}
	}
	return renderBlocks
}

func linuxdoTextBlockMetrics(kind string, contentW int) (size float64, lineH, innerW, extraH int) {
	size, lineH, innerW = 24, 34, contentW
	switch kind {
	case "heading":
		return 28, 40, contentW, 4
	case "quote", "poll":
		return 22, 32, maxInt(1, contentW-44), 28
	case "code":
		return 20, 30, maxInt(1, contentW-36), 30
	case "list", "table":
		return 23, 33, contentW, 4
	case "attachment", "link":
		return 21, 30, maxInt(1, contentW-82), 28
	default:
		return size, lineH, innerW, extraH
	}
}

func linuxdoImageDisplaySize(img image.Image, maxW, maxH int) (int, int) {
	if maxW <= 0 {
		maxW = 1
	}
	if maxH <= 0 {
		maxH = 1
	}
	if img == nil {
		return maxW, 160
	}
	bounds := img.Bounds()
	iw, ih := bounds.Dx(), bounds.Dy()
	if iw <= 0 || ih <= 0 {
		return maxW, 160
	}
	scale := 1.0
	if iw > maxW {
		scale = float64(maxW) / float64(iw)
	}
	if scaledH := float64(ih) * scale; scaledH > float64(maxH) {
		scale = float64(maxH) / float64(ih)
	}
	return maxInt(1, int(float64(iw)*scale)), maxInt(1, int(float64(ih)*scale))
}

func linuxdoConstrainRenderBlocks(blocks []linuxdoRenderBlock, maxHeight int) []linuxdoRenderBlock {
	if maxHeight <= 0 || linuxdoRenderBlocksHeight(blocks) <= maxHeight {
		return blocks
	}
	fixedHeight := 0
	imageHeight := 0
	for i, block := range blocks {
		if i > 0 {
			fixedHeight += linuxdoRenderBlockGap(blocks[i-1], block)
		}
		if block.kind == "image" {
			imageHeight += block.height
		} else {
			fixedHeight += block.height
		}
	}
	available := maxHeight - fixedHeight
	if imageHeight <= 0 || available <= 0 {
		return linuxdoTrimRenderBlocks(blocks, maxHeight)
	}
	scale := float64(available) / float64(imageHeight)
	if scale >= 1 {
		return blocks
	}
	for i := range blocks {
		if blocks[i].kind != "image" {
			continue
		}
		blocks[i].width = maxInt(1, int(float64(blocks[i].width)*scale))
		blocks[i].height = maxInt(1, int(float64(blocks[i].height)*scale))
	}
	if linuxdoRenderBlocksHeight(blocks) > maxHeight {
		return linuxdoTrimRenderBlocks(blocks, maxHeight)
	}
	return blocks
}

func linuxdoTrimRenderBlocks(blocks []linuxdoRenderBlock, maxHeight int) []linuxdoRenderBlock {
	const noticeHeight = 64
	out := make([]linuxdoRenderBlock, 0, len(blocks))
	height := 0
	for _, block := range blocks {
		gap := 0
		if len(out) > 0 {
			gap = linuxdoRenderBlockGap(out[len(out)-1], block)
		}
		if height+gap+block.height+noticeHeight > maxHeight {
			notice := linuxdoRenderBlock{
				kind: "attachment", text: "内容过长，剩余内容请打开原帖查看。", lines: []string{"内容过长，剩余内容请打开原帖查看。"}, height: noticeHeight,
			}
			return append(out, notice)
		}
		out = append(out, block)
		height += gap + block.height
	}
	return out
}

func linuxdoRenderBlocksHeight(blocks []linuxdoRenderBlock) int {
	height := 0
	for i, block := range blocks {
		if i > 0 {
			height += linuxdoRenderBlockGap(blocks[i-1], block)
		}
		height += block.height
	}
	return height
}

func linuxdoRenderBlockGap(previous, current linuxdoRenderBlock) int {
	gap := 16
	if previous.kind == "image" || current.kind == "image" {
		gap = 22
	}
	if current.kind == "heading" {
		gap = 26
	}
	return gap
}

func linuxdoDrawRenderBlock(dc *gg.Context, fontBytes, headingFontBytes []byte, block linuxdoRenderBlock, x, y, contentW int, theme keylolCardTheme) {
	switch block.kind {
	case "image":
		linuxdoDrawContentImage(dc, fontBytes, block, x, y, contentW, theme)
	case "heading":
		yy := y + 30
		for _, line := range block.lines {
			drawInlineEmoji(dc, headingFontBytes, 28, theme.Title, line, float64(x), float64(yy))
			yy += 40
		}
	case "quote", "poll":
		linuxdoDrawInsetTextBlock(dc, fontBytes, block, x, y, contentW, theme)
	case "code":
		linuxdoDrawCodeBlock(dc, fontBytes, block, x, y, contentW, theme)
	case "attachment", "link":
		linuxdoDrawAttachmentBlock(dc, fontBytes, block, x, y, contentW, theme)
	default:
		yy := y + 26
		for _, line := range block.lines {
			drawInlineEmoji(dc, fontBytes, 24, theme.Body, line, float64(x), float64(yy))
			yy += 34
		}
	}
}

func linuxdoDrawContentImage(dc *gg.Context, fontBytes []byte, block linuxdoRenderBlock, x, y, contentW int, theme keylolCardTheme) {
	drawX := x + (contentW-block.width)/2
	setRGB(dc, theme.Line)
	dc.DrawRoundedRectangle(float64(drawX), float64(y), float64(block.width), float64(block.height), 10)
	dc.ClipPreserve()
	if block.img == nil {
		dc.Fill()
		drawInlineEmoji(dc, fontBytes, 18, theme.Muted, "图片加载失败", float64(drawX+18), float64(y+block.height/2+6))
	} else {
		dc.Fill()
		fit := imaging.Fit(block.img, block.width, block.height, imaging.Lanczos)
		bounds := fit.Bounds()
		dc.DrawImage(fit, drawX+(block.width-bounds.Dx())/2, y+(block.height-bounds.Dy())/2)
	}
	dc.ResetClip()
	setRGB(dc, theme.Line)
	dc.SetLineWidth(1)
	dc.DrawRoundedRectangle(float64(drawX)+0.5, float64(y)+0.5, float64(block.width)-1, float64(block.height)-1, 10)
	dc.Stroke()
}

func linuxdoDrawInsetTextBlock(dc *gg.Context, fontBytes []byte, block linuxdoRenderBlock, x, y, contentW int, theme keylolCardTheme) {
	bg := linuxdoInsetBlockColor(theme)
	setRGB(dc, bg)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(contentW), float64(block.height), 8)
	dc.Fill()
	accent := linuxdoLinkColor(theme)
	setRGB(dc, accent)
	dc.DrawRoundedRectangle(float64(x+12), float64(y+12), 4, float64(maxInt(4, block.height-24)), 2)
	dc.Fill()
	yy := y + 25
	for _, line := range block.lines {
		drawInlineEmoji(dc, fontBytes, 22, theme.Body, line, float64(x+28), float64(yy))
		yy += 32
	}
}

func linuxdoDrawCodeBlock(dc *gg.Context, fontBytes []byte, block linuxdoRenderBlock, x, y, contentW int, theme keylolCardTheme) {
	bg := color.RGBA{R: 30, G: 35, B: 45, A: 255}
	textColor := color.RGBA{R: 232, G: 236, B: 243, A: 255}
	if keylolThemeDark(theme) {
		bg = color.RGBA{R: 12, G: 15, B: 21, A: 255}
	}
	setRGB(dc, bg)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(contentW), float64(block.height), 8)
	dc.Fill()
	yy := y + 25
	for _, line := range block.lines {
		drawInlineEmoji(dc, fontBytes, 20, textColor, line, float64(x+18), float64(yy))
		yy += 30
	}
}

func linuxdoDrawAttachmentBlock(dc *gg.Context, fontBytes []byte, block linuxdoRenderBlock, x, y, contentW int, theme keylolCardTheme) {
	bg := linuxdoInsetBlockColor(theme)
	setRGB(dc, bg)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(contentW), float64(block.height), 8)
	dc.FillPreserve()
	setRGB(dc, theme.Line)
	dc.SetLineWidth(1)
	dc.Stroke()
	accent := linuxdoLinkColor(theme)
	setRGB(dc, accent)
	dc.DrawRoundedRectangle(float64(x+14), float64(y+14), 48, 30, 7)
	dc.Fill()
	drawInlineEmoji(dc, fontBytes, 15, color.RGBA{R: 255, G: 255, B: 255, A: 255}, "附件", float64(x+23), float64(y+35))
	yy := y + 25
	for _, line := range block.lines {
		drawInlineEmoji(dc, fontBytes, 21, theme.Body, line, float64(x+74), float64(yy))
		yy += 30
	}
}

func linuxdoInsetBlockColor(theme keylolCardTheme) color.RGBA {
	if keylolThemeDark(theme) {
		return color.RGBA{R: clampColorInt(int(theme.Panel.R) + 20), G: clampColorInt(int(theme.Panel.G) + 20), B: clampColorInt(int(theme.Panel.B) + 20), A: 255}
	}
	return color.RGBA{R: clampColorInt(int(theme.Panel.R) - 4), G: clampColorInt(int(theme.Panel.G) - 5), B: clampColorInt(int(theme.Panel.B) - 6), A: 255}
}

func linuxdoLinkColor(theme keylolCardTheme) color.RGBA {
	if keylolThemeDark(theme) {
		return color.RGBA{R: 105, G: 169, B: 255, A: 255}
	}
	return color.RGBA{R: 48, G: 112, B: 220, A: 255}
}
