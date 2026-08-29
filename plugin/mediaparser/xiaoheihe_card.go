package mediaparser

import (
	"image"
	"image/color"
	"strings"

	"github.com/FloatTech/gg"
	"github.com/disintegration/imaging"
	"github.com/sirupsen/logrus"
)

const (
	xiaoheiheCardWidth            = 820
	xiaoheiheCardPadding          = 34
	xiaoheiheCardContentWidth     = xiaoheiheCardWidth - xiaoheiheCardPadding*2
	xiaoheiheMaxContentHeight     = 10400
	xiaoheiheTruncatedNotice      = "内容较长，后续内容请打开原帖查看"
	xiaoheiheImageMaxHeight       = 920
	xiaoheiheFallbackBlockSpacing = 26
)

type xiaoheiheRenderBlock struct {
	kind   string
	lines  []string
	img    image.Image
	width  int
	height int
}

func renderXiaoheiheThreadCard(meta mediaMeta, fontBytes []byte) (string, error) {
	const (
		titleSize = 36.0
		bodySize  = 27.0
		metaSize  = 20.0
		titleLH   = 50
		bodyLH    = 42
	)
	contentW := xiaoheiheCardContentWidth
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	measure := gg.NewContext(xiaoheiheCardWidth, 100)
	mustFont(measure, bodyFontBytes, bodySize)
	blocks := xiaoheiheBlocksForRender(meta)
	renderBlocks := make([]xiaoheiheRenderBlock, 0, len(blocks))
	for _, block := range blocks {
		if xiaoheiheRenderContentHeight(renderBlocks) > xiaoheiheMaxContentHeight {
			break
		}
		switch block.Kind {
		case "text":
			lines := wrapTextByPixels(measure, bodyFontBytes, bodySize, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, xiaoheiheRenderBlock{kind: "text", lines: lines, height: len(lines) * bodyLH})
			}
		case "image":
			img := fetchCardImage(block.URL, meta.ImageHeads)
			if img == nil {
				continue
			}
			w, h := xiaoheiheImageDrawSize(img, contentW)
			renderBlocks = append(renderBlocks, xiaoheiheRenderBlock{kind: "image", img: img, width: w, height: h})
		case "video":
			renderBlocks = append(renderBlocks, xiaoheiheRenderBlock{kind: "video", height: 258})
		}
	}
	originalCount := len(renderBlocks)
	if trimmed, truncated := trimXiaoheiheRenderBlocks(renderBlocks, xiaoheiheMaxContentHeight); truncated {
		renderBlocks = trimmed
		logrus.Infof("[mediaparser] xiaoheihe_card_truncated title=%q blocks=%d rendered=%d max_content_height=%d", truncate(meta.Title, 80), originalCount, len(renderBlocks), xiaoheiheMaxContentHeight)
	}

	titleLines := wrapTextByPixels(measure, fontBytes, titleSize, firstNonEmpty(meta.Title, "小黑盒帖子"), float64(contentW))
	if len(titleLines) == 0 {
		titleLines = []string{"小黑盒帖子"}
	}
	contentH := 0
	for i, block := range renderBlocks {
		if i > 0 {
			contentH += xiaoheiheBlockSpacing(renderBlocks[i-1], block)
		}
		contentH += block.height
	}
	headerH := 138 + len(titleLines)*titleLH + 46
	footerH := 76
	height := xiaoheiheCardPadding*2 + headerH + contentH + footerH

	dc := gg.NewContext(xiaoheiheCardWidth, height)
	dc.SetRGB255(245, 246, 247)
	dc.Clear()
	x, y := xiaoheiheCardPadding, xiaoheiheCardPadding
	dc.SetRGB255(255, 255, 255)
	dc.DrawRectangle(float64(x), float64(y), float64(contentW), float64(height-xiaoheiheCardPadding*2))
	dc.Fill()

	avatar := fetchCardImage(meta.Avatar, meta.ImageHeads)
	drawAvatar(dc, fontBytes, avatar, x, y, 74, meta.Author)
	mustFont(dc, fontBytes, 25)
	dc.SetRGB255(37, 38, 40)
	dc.DrawString(firstNonEmpty(meta.Author, "小黑盒用户"), float64(x+92), float64(y+29))
	mustFont(dc, bodyFontBytes, metaSize)
	dc.SetRGB255(144, 148, 153)
	dc.DrawString(firstNonEmpty(meta.Timestamp, "小黑盒"), float64(x+92), float64(y+63))
	dc.SetRGB255(255, 112, 36)
	dc.DrawRoundedRectangle(float64(x+contentW-102), float64(y+10), 102, 38, 7)
	dc.Fill()
	mustFont(dc, fontBytes, 18)
	dc.SetRGB255(255, 255, 255)
	dc.DrawStringAnchored("小黑盒", float64(x+contentW-51), float64(y+31), 0.5, 0.5)
	y += 116

	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, titleSize, color.RGBA{R: 30, G: 31, B: 33, A: 255}, line, float64(x), float64(y))
		y += titleLH
	}
	y += 18
	dc.SetRGB255(235, 237, 239)
	dc.DrawRectangle(float64(x), float64(y), float64(contentW), 1)
	dc.Fill()
	y += 32

	for i, block := range renderBlocks {
		if i > 0 {
			y += xiaoheiheBlockSpacing(renderBlocks[i-1], block)
		}
		switch block.kind {
		case "notice":
			dc.SetRGB255(246, 247, 248)
			dc.DrawRoundedRectangle(float64(x), float64(y), float64(contentW), float64(block.height), 6)
			dc.Fill()
			mustFont(dc, bodyFontBytes, 20)
			dc.SetRGB255(132, 137, 143)
			dc.DrawStringAnchored(block.lines[0], float64(x+contentW/2), float64(y+block.height/2), 0.5, 0.5)
			y += block.height
		case "text":
			for _, line := range block.lines {
				drawInlineEmoji(dc, bodyFontBytes, bodySize, color.RGBA{R: 56, G: 58, B: 61, A: 255}, line, float64(x), float64(y))
				y += bodyLH
			}
		case "image":
			drawXiaoheiheImage(dc, block.img, x, y, block.width, block.height)
			y += block.height
		case "video":
			drawXiaoheiheVideoPlaceholder(dc, fontBytes, x, y, contentW, block.height)
			y += block.height
		}
	}

	mustFont(dc, bodyFontBytes, 18)
	dc.SetRGB255(157, 161, 166)
	dc.DrawStringAnchored("来自小黑盒", float64(x+contentW/2), float64(height-xiaoheiheCardPadding-28), 0.5, 0.5)
	return saveCardPNG(dc, meta)
}

func xiaoheiheBlocksForRender(meta mediaMeta) []xiaoheiheBlock {
	if len(meta.XiaoheiheBlocks) > 0 {
		return meta.XiaoheiheBlocks
	}
	blocks := []xiaoheiheBlock{}
	if text := strings.TrimSpace(meta.Desc); text != "" {
		blocks = append(blocks, xiaoheiheBlock{Kind: "text", Text: text})
	}
	for _, group := range meta.ImageURLs {
		if len(group) > 0 && strings.TrimSpace(group[0]) != "" {
			blocks = append(blocks, xiaoheiheBlock{Kind: "image", URL: group[0]})
		}
	}
	return blocks
}

func xiaoheiheImageDrawSize(img image.Image, maxWidth int) (int, int) {
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return maxWidth, 1
	}
	w := maxWidth
	h := int(float64(w) * float64(b.Dy()) / float64(b.Dx()))
	if h > xiaoheiheImageMaxHeight {
		h = xiaoheiheImageMaxHeight
		w = int(float64(h) * float64(b.Dx()) / float64(b.Dy()))
	}
	return maxInt(1, minInt(maxWidth, w)), maxInt(1, h)
}

func xiaoheiheBlockSpacing(prev, cur xiaoheiheRenderBlock) int {
	if prev.kind == "text" && cur.kind == "text" {
		return 16
	}
	if prev.kind == "image" || cur.kind == "image" {
		return 30
	}
	return xiaoheiheFallbackBlockSpacing
}

func trimXiaoheiheRenderBlocks(blocks []xiaoheiheRenderBlock, maxHeight int) ([]xiaoheiheRenderBlock, bool) {
	if len(blocks) == 0 || maxHeight <= 0 {
		return blocks, false
	}
	const noticeHeight = 44
	budget := maxHeight - noticeHeight - xiaoheiheFallbackBlockSpacing
	used := 0
	out := make([]xiaoheiheRenderBlock, 0, len(blocks))
	for _, block := range blocks {
		next := block.height
		if len(out) > 0 {
			next += xiaoheiheBlockSpacing(out[len(out)-1], block)
		}
		if used+next > budget {
			out = append(out, xiaoheiheRenderBlock{kind: "notice", lines: []string{xiaoheiheTruncatedNotice}, height: noticeHeight})
			return out, true
		}
		out = append(out, block)
		used += next
	}
	return blocks, false
}

func xiaoheiheRenderContentHeight(blocks []xiaoheiheRenderBlock) int {
	height := 0
	for i, block := range blocks {
		if i > 0 {
			height += xiaoheiheBlockSpacing(blocks[i-1], block)
		}
		height += block.height
	}
	return height
}

func drawXiaoheiheImage(dc *gg.Context, img image.Image, x, y, w, h int) {
	if img == nil {
		return
	}
	// The original media is scaled as a whole; it is never cropped into a gallery cell.
	resized := imaging.Resize(img, w, h, imaging.Lanczos)
	dc.DrawImage(resized, x, y)
}

func drawXiaoheiheVideoPlaceholder(dc *gg.Context, fontBytes []byte, x, y, w, h int) {
	dc.SetRGB255(32, 34, 37)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 8)
	dc.Fill()
	dc.SetRGB255(255, 112, 36)
	dc.DrawCircle(float64(x+w/2), float64(y+h/2), 34)
	dc.Fill()
	dc.SetRGB255(255, 255, 255)
	dc.MoveTo(float64(x+w/2-7), float64(y+h/2-14))
	dc.LineTo(float64(x+w/2-7), float64(y+h/2+14))
	dc.LineTo(float64(x+w/2+17), float64(y+h/2))
	dc.ClosePath()
	dc.Fill()
	mustFont(dc, fontBytes, 21)
	dc.SetRGB255(221, 223, 226)
	dc.DrawStringAnchored("视频内容请打开原帖播放", float64(x+w/2), float64(y+h-32), 0.5, 0.5)
}
