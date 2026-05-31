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
	if meta.Platform == "steam" {
		return renderSteamGameCard(meta, fontBytes)
	}
	if shouldConsiderLongImageCard(meta) {
		return renderUnifiedLongImageCard(meta, fontBytes)
	}
	if shouldRenderAsGalleryCard(meta) {
		return renderUnifiedGalleryCard(meta, fontBytes)
	}
	return renderUnifiedVideoCard(meta, fontBytes)
}

func keylolBodyFontBytes(fallback []byte) []byte {
	fontBytes, err := file.GetLazyData(text.FontFile, control.Md5File, true)
	if err != nil || len(fontBytes) == 0 {
		return fallback
	}
	return fontBytes
}

func renderSteamGameCard(meta mediaMeta, fontBytes []byte) (string, error) {
	w, h := 920, 520
	dc := gg.NewContext(w, h)
	dc.SetRGB255(26, 26, 36)
	dc.Clear()

	bg := fetchCardImage(firstNonEmpty(meta.SteamHeaderImage, meta.Cover), nil)
	if bg != nil {
		bg = imaging.Fill(bg, w, h, imaging.Center, imaging.Lanczos)
		bg = imaging.Blur(bg, 18)
		dc.DrawImage(bg, 0, 0)
		dc.SetRGBA255(10, 14, 24, 178)
		dc.DrawRectangle(0, 0, float64(w), float64(h))
		dc.Fill()
	}

	panelX, panelY, panelW, panelH := 54, 54, w-108, h-108
	for i := 18; i >= 1; i-- {
		dc.SetRGBA255(0, 0, 0, 5+i*2)
		dc.DrawRoundedRectangle(float64(panelX), float64(panelY+i), float64(panelW), float64(panelH), 30)
		dc.Fill()
	}
	dc.SetRGBA255(255, 255, 255, 24)
	dc.DrawRoundedRectangle(float64(panelX), float64(panelY), float64(panelW), float64(panelH), 30)
	dc.FillPreserve()
	dc.SetLineWidth(1.5)
	dc.SetRGBA255(255, 255, 255, 42)
	dc.Stroke()
	drawSteamLogoVariant(dc, fontBytes, float64(panelX+panelW-58), float64(panelY+48), 54, "steam_dark")

	coverX, coverY, coverW, coverH := panelX+28, panelY+30, 250, panelH-60
	if cover := fetchCardImage(meta.Cover, nil); cover != nil {
		cover = imaging.Fill(cover, coverW, coverH, imaging.Center, imaging.Lanczos)
		dc.DrawRoundedRectangle(float64(coverX), float64(coverY), float64(coverW), float64(coverH), 18)
		dc.ClipPreserve()
		dc.DrawImage(cover, coverX, coverY)
		dc.ResetClip()
		dc.SetLineWidth(1.5)
		dc.SetRGBA255(255, 255, 255, 38)
		dc.Stroke()
	} else {
		dc.SetRGBA255(255, 255, 255, 26)
		dc.DrawRoundedRectangle(float64(coverX), float64(coverY), float64(coverW), float64(coverH), 18)
		dc.Fill()
	}

	textX := coverX + coverW + 32
	textW := panelX + panelW - textX - 78
	y := float64(coverY + 30)
	title := firstNonEmpty(meta.Title, "Steam "+meta.SteamAppID)
	for _, line := range wrapDisplayTextByPixels(fontBytes, 42, title, float64(textW), 2) {
		drawInlineEmoji(dc, fontBytes, 42, color.RGBA{R: 255, G: 255, B: 255, A: 255}, line, float64(textX), y)
		y += 50
	}
	subtitle := strings.TrimSpace(meta.SteamSubtitle)
	if subtitle != "" && !strings.EqualFold(strings.ReplaceAll(subtitle, " ", "_"), strings.ReplaceAll(title, " ", "_")) {
		drawInlineEmoji(dc, fontBytes, 23, color.RGBA{R: 210, G: 220, B: 232, A: 190}, subtitle, float64(textX), y+4)
		y += 34
	}
	if len(meta.SteamGenres) > 0 {
		genres := strings.Join(meta.SteamGenres, " | ")
		for _, line := range wrapDisplayTextByPixels(fontBytes, 21, genres, float64(textW), 2) {
			drawInlineEmoji(dc, fontBytes, 21, color.RGBA{R: 190, G: 206, B: 220, A: 150}, line, float64(textX), y)
			y += 28
		}
	}
	if meta.Desc != "" {
		y += 10
		for _, line := range wrapDisplayTextByPixels(fontBytes, 22, meta.Desc, float64(textW), 3) {
			drawInlineEmoji(dc, fontBytes, 22, color.RGBA{R: 225, G: 232, B: 238, A: 190}, line, float64(textX), y)
			y += 31
		}
	}

	ratingY := float64(panelY + panelH - 108)
	if meta.SteamReviewPercent > 0 || meta.SteamReviewSummary != "" {
		review := strings.TrimSpace(meta.SteamReviewSummary)
		if meta.SteamReviewPercent > 0 {
			review = fmt.Sprintf("%d%% %s", meta.SteamReviewPercent, firstNonEmpty(review, "好评"))
		}
		drawInlineEmoji(dc, fontBytes, 26, color.RGBA{R: 16, G: 185, B: 129, A: 255}, "★ "+review, float64(textX), ratingY)
		ratingY += 34
	}

	priceY := float64(panelY + panelH - 42)
	current := firstNonEmpty(meta.SteamPriceCurrent, "价格未知")
	drawInlineEmoji(dc, fontBytes, 36, color.RGBA{R: 255, G: 255, B: 255, A: 255}, current, float64(textX), priceY)
	cursor := float64(textX) + 18
	mustFont(dc, fontBytes, 36)
	if tw, _ := dc.MeasureString(current); tw > 0 {
		cursor += tw
	}
	if meta.SteamPriceOriginal != "" && meta.SteamPriceOriginal != current {
		mustFont(dc, fontBytes, 19)
		dc.SetRGBA255(255, 255, 255, 82)
		dc.DrawStringAnchored(meta.SteamPriceOriginal, cursor, priceY+2, 0, 0.5)
		ow, _ := dc.MeasureString(meta.SteamPriceOriginal)
		dc.SetRGBA255(255, 255, 255, 72)
		dc.SetLineWidth(2)
		dc.DrawLine(cursor, priceY+2, cursor+ow, priceY+2)
		dc.Stroke()
		cursor += ow + 14
	}
	if meta.SteamDiscount > 0 {
		discount := fmt.Sprintf("-%d%%", meta.SteamDiscount)
		mustFont(dc, fontBytes, 20)
		dw, _ := dc.MeasureString(discount)
		dc.SetRGBA255(245, 158, 11, 42)
		dc.DrawRoundedRectangle(cursor, priceY-18, dw+18, 30, 7)
		dc.Fill()
		drawInlineEmoji(dc, fontBytes, 20, color.RGBA{R: 245, G: 158, B: 11, A: 255}, discount, cursor+9, priceY-2)
	}
	return saveCardPNG(dc, meta)
}

func drawSteamBadge(dc *gg.Context, fontBytes []byte, cx, cy float64) {
	drawOfficialSteamLogo(dc, fontBytes, cx, cy, 54)
}

func drawOfficialSteamLogo(dc *gg.Context, fontBytes []byte, cx, cy float64, size int) {
	logoName := "steam_light"
	if keylolThemeDark(keylolCardThemeNow()) {
		logoName = "steam_dark"
	}
	drawSteamLogoVariant(dc, fontBytes, cx, cy, size, logoName)
}

func drawSteamLogoVariant(dc *gg.Context, fontBytes []byte, cx, cy float64, size int, logoName string) {
	img := loadPlatformLogo(logoName)
	if img == nil {
		img = fetchCachedCardImage("official-steam-logo-v4", "https://upload.wikimedia.org/wikipedia/commons/thumb/8/83/Steam_icon_logo.svg/512px-Steam_icon_logo.svg.png", nil)
	}
	if img != nil {
		fit := imaging.Fit(img, size, size, imaging.Lanczos)
		dc.DrawImageAnchored(fit, int(cx), int(cy), 0.5, 0.5)
		return
	}
	drawSteamLogoFallback(dc, cx, cy, float64(size))
}

func drawSteamLogoFallback(dc *gg.Context, cx, cy, size float64) {
	r := size / 2
	for i := int(r); i >= 0; i-- {
		t := float64(i) / r
		dc.SetRGB255(int(19+4*t), int(135-105*t), int(184-138*t))
		dc.DrawCircle(cx, cy, float64(i))
		dc.Fill()
	}
	dc.SetRGB255(255, 255, 255)
	dc.SetLineWidth(size * 0.09)
	dc.DrawCircle(cx+size*0.17, cy-size*0.16, size*0.17)
	dc.Stroke()
	dc.SetLineWidth(size * 0.16)
	dc.DrawLine(cx-size*0.04, cy+size*0.06, cx+size*0.04, cy-size*0.02)
	dc.DrawLine(cx+size*0.04, cy-size*0.02, cx+size*0.22, cy-size*0.13)
	dc.Stroke()
	dc.DrawCircle(cx-size*0.2, cy+size*0.23, size*0.17)
	dc.Fill()
	dc.SetRGB255(9, 30, 72)
	dc.DrawCircle(cx-size*0.2, cy+size*0.23, size*0.09)
	dc.Fill()
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

const (
	unifiedCardW    = 760
	unifiedOuterPad = 24
)

type unifiedCardTheme struct {
	Dark          bool
	BG            color.RGBA
	Panel         color.RGBA
	Border        color.RGBA
	Title         color.RGBA
	Body          color.RGBA
	Muted         color.RGBA
	MediaBG       color.RGBA
	BoxBG         color.RGBA
	BoxBorder     color.RGBA
	Topic         color.RGBA
	PlatformLogoY float64
}

func unifiedThemeNow() unifiedCardTheme {
	dark := keylolThemeDark(keylolCardThemeNow())
	if dark {
		return unifiedCardTheme{
			Dark:      true,
			BG:        color.RGBA{R: 24, G: 25, B: 28, A: 255},
			Panel:     color.RGBA{R: 50, G: 51, B: 55, A: 255},
			Border:    color.RGBA{R: 108, G: 110, B: 116, A: 255},
			Title:     color.RGBA{R: 255, G: 255, B: 255, A: 255},
			Body:      color.RGBA{R: 201, G: 204, B: 208, A: 255},
			Muted:     color.RGBA{R: 148, G: 153, B: 160, A: 255},
			MediaBG:   color.RGBA{R: 42, G: 45, B: 50, A: 255},
			BoxBG:     color.RGBA{R: 70, G: 72, B: 76, A: 255},
			BoxBorder: color.RGBA{R: 92, G: 94, B: 100, A: 255},
			Topic:     color.RGBA{R: 100, G: 150, B: 220, A: 255},
		}
	}
	return unifiedCardTheme{
		BG:        color.RGBA{R: 241, G: 242, B: 245, A: 255},
		Panel:     color.RGBA{R: 255, G: 255, B: 255, A: 255},
		Border:    color.RGBA{R: 227, G: 229, B: 231, A: 255},
		Title:     color.RGBA{R: 24, G: 25, B: 28, A: 255},
		Body:      color.RGBA{R: 97, G: 102, B: 109, A: 255},
		Muted:     color.RGBA{R: 148, G: 153, B: 160, A: 255},
		MediaBG:   color.RGBA{R: 227, G: 229, B: 231, A: 255},
		BoxBG:     color.RGBA{R: 246, G: 247, B: 248, A: 255},
		BoxBorder: color.RGBA{R: 227, G: 229, B: 231, A: 255},
		Topic:     color.RGBA{R: 90, G: 134, B: 206, A: 255},
	}
}

func unifiedLayout() (panelX, panelY, panelW, panelPad, contentW int) {
	panelX, panelY = unifiedOuterPad, unifiedOuterPad
	panelW = unifiedCardW - unifiedOuterPad*2
	panelPad = 38
	contentW = panelW - panelPad*2
	return
}

func renderUnifiedVideoCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	tm := unifiedThemeNow()
	panelX, panelY, panelW, panelPad, contentW := unifiedLayout()
	coverImg := fetchCardImage(firstCardCover(meta), meta.ImageHeads)
	avatarImg := fetchCardImage(meta.Avatar, meta.ImageHeads)
	title := firstNonEmpty(meta.Title, meta.Desc, "媒体解析")
	titleLines := wrapDisplayTextByPixels(fontBytes, 31, title, float64(contentW), 2)
	if len(titleLines) == 0 {
		titleLines = []string{title}
	}
	summaryLines := wrapTextByPixels(gg.NewContext(unifiedCardW, 100), bodyFontBytes, 22, cardSummaryText(meta), float64(contentW-34))
	if len(summaryLines) > 5 {
		summaryLines = summaryLines[:5]
	}
	if len(summaryLines) == 0 {
		summaryLines = []string{"该视频暂无总结"}
	}
	headerH := 86
	titleY := panelY + panelPad + headerH + 38
	coverY := titleY + len(titleLines)*42 + 16
	coverH := int(float64(contentW) * 9 / 16)
	summaryY := coverY + coverH + 26
	summaryH := unifiedTextBoxHeight(len(summaryLines))
	panelH := summaryY + summaryH + panelPad - panelY
	height := panelY + panelH + unifiedOuterPad

	dc := gg.NewContext(unifiedCardW, height)
	drawUnifiedBackground(dc, tm, unifiedCardW, height)
	drawUnifiedPanel(dc, tm, panelX, panelY, panelW, panelH)
	drawUnifiedHeader(dc, fontBytes, tm, meta, avatarImg, panelX+panelPad, panelY+panelPad, contentW)
	drawUnifiedTitle(dc, fontBytes, tm, titleLines, panelX+panelPad, titleY)
	drawUnifiedMediaCell(dc, tm, coverImg, panelX+panelPad, coverY, contentW, coverH, shouldDrawPlayOverlay(meta))
	drawUnifiedTextBox(dc, bodyFontBytes, tm, "视频智能总结", summaryLines, panelX+panelPad, summaryY, contentW)
	return saveCardPNG(dc, meta)
}

func renderUnifiedGalleryCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	tm := unifiedThemeNow()
	panelX, panelY, panelW, panelPad, contentW := unifiedLayout()
	avatarImg := fetchCardImage(meta.Avatar, meta.ImageHeads)
	imageURLs := make([]string, 0, len(meta.ImageURLs))
	for _, group := range meta.ImageURLs {
		if len(group) > 0 {
			imageURLs = append(imageURLs, group[0])
		}
		if len(imageURLs) >= 9 {
			break
		}
	}
	images := compactCardImages(fetchCardImages(imageURLs, meta.ImageHeads))
	title := firstNonEmpty(meta.Title, "媒体解析")
	titleLines := wrapDisplayTextByPixels(fontBytes, 31, title, float64(contentW), 2)
	if len(titleLines) == 0 {
		titleLines = []string{title}
	}
	descLines := wrapTextByPixels(gg.NewContext(unifiedCardW, 100), bodyFontBytes, 22, meta.Desc, float64(contentW-34))
	if len(descLines) > 12 {
		descLines = descLines[:12]
	}
	headerH := 86
	titleY := panelY + panelPad + headerH + 38
	gridY := titleY + len(titleLines)*42 + 18
	gridH := galleryGridHeightForImages(images, contentW)
	descY := gridY
	if gridH > 0 {
		descY += gridH + 26
	}
	descH := 0
	if len(descLines) > 0 {
		descH = unifiedTextBoxHeight(len(descLines))
	}
	panelH := descY + descH + panelPad - panelY
	height := panelY + panelH + unifiedOuterPad

	dc := gg.NewContext(unifiedCardW, height)
	drawUnifiedBackground(dc, tm, unifiedCardW, height)
	drawUnifiedPanel(dc, tm, panelX, panelY, panelW, panelH)
	drawUnifiedHeader(dc, fontBytes, tm, meta, avatarImg, panelX+panelPad, panelY+panelPad, contentW)
	drawUnifiedTitle(dc, fontBytes, tm, titleLines, panelX+panelPad, titleY)
	drawGalleryGrid(dc, images, panelX+panelPad, gridY, contentW, gridH)
	if len(descLines) > 0 {
		drawUnifiedTextBox(dc, bodyFontBytes, tm, "正文", descLines, panelX+panelPad, descY, contentW)
	}
	return saveCardPNG(dc, meta)
}

func renderUnifiedLongImageCard(meta mediaMeta, fontBytes []byte) (string, error) {
	bodyFontBytes := keylolBodyFontBytes(fontBytes)
	img := fetchCardImage(meta.ImageURLs[0][0], meta.ImageHeads)
	if !shouldRenderLongImageCard(meta, img) {
		return renderUnifiedGalleryCard(meta, fontBytes)
	}
	tm := unifiedThemeNow()
	panelX, panelY, panelW, panelPad, contentW := unifiedLayout()
	avatarImg := fetchCardImage(meta.Avatar, meta.ImageHeads)
	title := firstNonEmpty(meta.Title, "媒体解析")
	titleLines := wrapDisplayTextByPixels(fontBytes, 31, title, float64(contentW), 2)
	descLines := wrapTextByPixels(gg.NewContext(unifiedCardW, 100), bodyFontBytes, 22, meta.Desc, float64(contentW-34))
	if len(descLines) > 12 {
		descLines = descLines[:12]
	}
	headerH := 86
	titleY := panelY + panelPad + headerH + 38
	textBoxY := titleY + len(titleLines)*42 + 18
	imageY := textBoxY
	if len(descLines) > 0 {
		imageY += unifiedTextBoxHeight(len(descLines)) + 18
	}
	imageH := longImageCardHeight(img, contentW)
	if imageH < 360 {
		imageH = 360
	}
	panelH := imageY + imageH + panelPad - panelY
	height := panelY + panelH + unifiedOuterPad

	dc := gg.NewContext(unifiedCardW, height)
	drawUnifiedBackground(dc, tm, unifiedCardW, height)
	drawUnifiedPanel(dc, tm, panelX, panelY, panelW, panelH)
	drawUnifiedHeader(dc, fontBytes, tm, meta, avatarImg, panelX+panelPad, panelY+panelPad, contentW)
	drawUnifiedTitle(dc, fontBytes, tm, titleLines, panelX+panelPad, titleY)
	if len(descLines) > 0 {
		drawUnifiedTextBox(dc, bodyFontBytes, tm, "正文", descLines, panelX+panelPad, textBoxY, contentW)
	}
	drawUnifiedImageCellContain(dc, tm, img, panelX+panelPad, imageY, contentW, imageH)
	return saveCardPNG(dc, meta)
}

func unifiedTextBoxHeight(lines int) int {
	if lines <= 0 {
		return 0
	}
	return 34 + lines*34 + 24
}

func drawUnifiedBackground(dc *gg.Context, tm unifiedCardTheme, w, h int) {
	setRGB(dc, tm.BG)
	dc.Clear()
}

func drawUnifiedPanel(dc *gg.Context, tm unifiedCardTheme, x, y, w, h int) {
	for i := 18; i >= 1; i-- {
		alpha := 2 + i
		if tm.Dark {
			alpha = 6 + i*2
		}
		dc.SetRGBA255(0, 0, 0, alpha)
		dc.DrawRoundedRectangle(float64(x), float64(y+i), float64(w), float64(h), 24)
		dc.Fill()
	}
	setRGB(dc, tm.Panel)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 24)
	dc.FillPreserve()
	dc.SetLineWidth(1.5)
	setRGB(dc, tm.Border)
	dc.Stroke()
}

func drawUnifiedHeader(dc *gg.Context, fontBytes []byte, tm unifiedCardTheme, meta mediaMeta, avatar image.Image, x, y, w int) {
	displayAuthor := truncate(firstNonEmpty(cardDisplayAuthor(meta.Author), "未知用户"), 18)
	drawUnifiedAvatar(dc, fontBytes, avatar, x, y, 72, displayAuthor, tm)
	drawInlineEmoji(dc, fontBytes, 26, tm.Title, displayAuthor, float64(x+92), float64(y+26))
	if meta.Timestamp != "" {
		drawInlineEmoji(dc, fontBytes, 20, tm.Muted, meta.Timestamp, float64(x+92), float64(y+58))
	}
	drawPlatformLogo(dc, fontBytes, meta.Platform, float64(x+w), float64(y+36))
}

func drawUnifiedAvatar(dc *gg.Context, fontBytes []byte, img image.Image, x, y, size int, author string, tm unifiedCardTheme) {
	border := 2
	if tm.Dark {
		border = 3
	}
	setRGB(dc, tm.Border)
	dc.DrawCircle(float64(x+size/2), float64(y+size/2), float64(size/2+border))
	dc.Fill()
	drawAvatar(dc, fontBytes, img, x, y, size, author)
}

func drawUnifiedTitle(dc *gg.Context, fontBytes []byte, tm unifiedCardTheme, lines []string, x, y int) {
	yy := float64(y)
	for _, line := range lines {
		drawInlineEmoji(dc, fontBytes, 31, tm.Title, line, float64(x), yy)
		yy += 42
	}
}

func drawUnifiedMediaCell(dc *gg.Context, tm unifiedCardTheme, img image.Image, x, y, w, h int, showPlay bool) {
	drawUnifiedImageCell(dc, tm, img, x, y, w, h, imaging.Center)
	if showPlay {
		drawUnifiedPlayOverlay(dc, tm, float64(x+w/2), float64(y+h/2))
	}
}

func drawUnifiedImageCell(dc *gg.Context, tm unifiedCardTheme, img image.Image, x, y, w, h int, anchor imaging.Anchor) {
	radius := 10.0
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), radius)
	dc.ClipPreserve()
	setRGB(dc, tm.MediaBG)
	dc.Fill()
	if img != nil {
		dc.DrawImage(imaging.Fill(img, w, h, anchor, imaging.Lanczos), x, y)
	}
	dc.ResetClip()
}

func drawUnifiedImageCellContain(dc *gg.Context, tm unifiedCardTheme, img image.Image, x, y, w, h int) {
	radius := 10.0
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), radius)
	dc.ClipPreserve()
	setRGB(dc, tm.MediaBG)
	dc.Fill()
	if img != nil {
		fit := imaging.Fit(img, w, h, imaging.Lanczos)
		b := fit.Bounds()
		dc.DrawImage(fit, x+(w-b.Dx())/2, y+(h-b.Dy())/2)
	}
	dc.ResetClip()
}

func drawUnifiedPlayOverlay(dc *gg.Context, tm unifiedCardTheme, cx, cy float64) {
	if tm.Dark {
		dc.SetRGBA255(255, 255, 255, 45)
	} else {
		dc.SetRGBA255(0, 0, 0, 102)
	}
	dc.DrawCircle(cx, cy, 32)
	dc.FillPreserve()
	dc.SetLineWidth(1.5)
	dc.SetRGBA255(255, 255, 255, 75)
	dc.Stroke()
	dc.SetRGB255(255, 255, 255)
	dc.MoveTo(cx-8, cy-15)
	dc.LineTo(cx-8, cy+15)
	dc.LineTo(cx+16, cy)
	dc.ClosePath()
	dc.Fill()
}

func drawUnifiedTextBox(dc *gg.Context, fontBytes []byte, tm unifiedCardTheme, title string, lines []string, x, y, w int) {
	h := unifiedTextBoxHeight(len(lines))
	setRGB(dc, tm.BoxBG)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 10)
	dc.FillPreserve()
	dc.SetLineWidth(1)
	setRGB(dc, tm.BoxBorder)
	dc.Stroke()
	drawInlineEmoji(dc, fontBytes, 22, tm.Title, "✨ "+title, float64(x+26), float64(y+30))
	yy := float64(y + 66)
	for _, line := range lines {
		drawTopicLineWithColors(dc, fontBytes, 22, line, float64(x+26), yy, tm.Body, tm.Topic)
		yy += 34
	}
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
		w           = 1320
		outerPad    = 32
		panelPad    = 50
		contentW    = w - outerPad*2 - panelPad*2
		titleSize   = 38.0
		bodySize    = 27.0
		toolbarSize = 17.0
		titleLH     = 56
		bodyLH      = 42
		toolbarLH   = 27
		imageGap    = 28
		blockGap    = 22
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
		case "spoiler":
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, 23, block.Text, float64(contentW-78))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "spoiler", text: block.Text, lines: lines, height: maxInt(52, len(lines)*32+18)})
			}
		case "hidden_label":
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, 22, block.Text, float64(contentW-78))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "hidden_label", text: block.Text, lines: lines, height: maxInt(44, len(lines)*28+16)})
			}
		case "currency_table":
			rows := keylolCurrencyTableRows(block.Desc)
			if len(rows) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "currency_table", text: block.Text, desc: block.Desc, lines: rows, height: keylolCurrencyTableHeight(len(rows))})
			}
		case "color_red", "color_green", "link":
			size := 28.0
			lineH := 40
			if block.Kind == "link" {
				size = 24
				lineH = 34
			}
			if block.Kind == "color_red" && i+1 < len(blocks) && blocks[i+1].Kind == "link" && keylolInlineStatusSuffix(blocks[i+1].Text) {
				text := strings.TrimSpace(block.Text + " " + blocks[i+1].Text)
				lines := wrapTextByPixels(dcMeasure, bodyFontBytes, size, text, float64(contentW))
				if len(lines) > 0 {
					renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "status_pair", text: strings.TrimSpace(block.Text), desc: strings.TrimSpace(blocks[i+1].Text), lines: lines, height: len(lines) * lineH})
					i++
				}
				continue
			}
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, size, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: block.Kind, text: block.Text, lines: lines, height: len(lines) * lineH})
			}
		case "code":
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, 22, block.Text, float64(contentW-52))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "code", text: block.Text, lines: lines, height: maxInt(58, len(lines)*32+30)})
			}
		case "steam_card":
			img := keylolPrepareImage(fetchCardImage(block.Cover, nil))
			if isBlankCardImage(img) {
				img = nil
			}
			descLines := wrapTextByPixels(dcMeasure, bodyFontBytes, 20, block.Desc, float64(contentW-keylolSteamCardCoverW-86))
			if len(descLines) > 3 {
				descLines = descLines[:3]
			}
			renderBlocks = append(renderBlocks, keylolRenderBlock{
				kind:   "steam_card",
				url:    block.URL,
				title:  firstNonEmpty(block.Title, "Steam 游戏"),
				desc:   block.Desc,
				cover:  block.Cover,
				img:    img,
				lines:  descLines,
				height: 208,
			})
		case "video_embed":
			img := keylolPrepareImage(fetchCardImage(block.Cover, nil))
			if isBlankCardImage(img) {
				img = nil
			}
			kind := "video_embed"
			height := 208
			if keylolShortVideoDesc(block.Desc) {
				kind = "video_embed_compact"
				height = keylolCompactVideoCardHeight(contentW, img != nil)
			}
			descLines := wrapTextByPixels(dcMeasure, bodyFontBytes, 20, block.Desc, float64(contentW-keylolVideoCardCoverW-86))
			if len(descLines) > 3 {
				descLines = descLines[:3]
			}
			renderBlocks = append(renderBlocks, keylolRenderBlock{
				kind:   kind,
				url:    block.URL,
				title:  firstNonEmpty(block.Title, "Bilibili 视频"),
				desc:   block.Desc,
				cover:  block.Cover,
				img:    img,
				lines:  descLines,
				width:  keylolVideoRenderWidth(kind, contentW),
				height: height,
			})
		case "asf_link":
			// ASF commands are delivered in the merged forward message; the card keeps the Steam game card only.
		case "toolbar":
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, toolbarSize, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "toolbar", text: block.Text, lines: lines, height: len(lines) * toolbarLH})
			}
		case "text":
			lines := wrapTextByPixels(dcMeasure, bodyFontBytes, bodySize, block.Text, float64(contentW))
			if len(lines) > 0 {
				renderBlocks = append(renderBlocks, keylolRenderBlock{kind: "text", text: block.Text, lines: lines, height: len(lines) * bodyLH})
			}
		}
	}
	titleLines := wrapTextByPixels(dcMeasure, fontBytes, titleSize, firstNonEmpty(meta.Title, "Keylol 帖子"), float64(contentW-150))
	contentH := 0
	for i, block := range renderBlocks {
		if i > 0 {
			contentH += keylolBlockGap(renderBlocks[i-1], block, blockGap, imageGap)
		}
		contentH += block.height
	}
	if contentH < 120 {
		contentH = 120
	}
	headerH := 116 + len(titleLines)*titleLH + 72
	footerH := 150
	panelH := panelPad + headerH + contentH + footerH
	height := panelH + outerPad*2

	dc := gg.NewContext(w, height)
	setRGB(dc, theme.BG)
	dc.Clear()
	drawKeylolPanel(dc, outerPad, outerPad, w-outerPad*2, panelH, theme)
	x := outerPad + panelPad
	y := outerPad + panelPad
	drawKeylolHeaderLogo(dc, fontBytes, float64(x), float64(y+6))
	y += 112
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, titleSize, theme.Title, line, float64(x), float64(y))
		y += titleLH
	}
	authorLine := firstNonEmpty(meta.Author, "Keylol 用户")
	drawKeylolAuthorLine(dc, fontBytes, authorLine, x, y+6)
	if meta.Timestamp != "" {
		mustFont(dc, fontBytes, 22)
		setRGB(dc, theme.Muted)
		ax, _ := dc.MeasureString(authorLine)
		dc.DrawStringAnchored("· "+meta.Timestamp, float64(x)+ax+64, float64(y+6), 0, 0.5)
	}
	y += 48
	setRGB(dc, theme.Line)
	dc.DrawRectangle(float64(x), float64(y), float64(contentW), 2)
	dc.Fill()
	y += 44
	for i, block := range renderBlocks {
		if i > 0 {
			y += keylolBlockGap(renderBlocks[i-1], block, blockGap, imageGap)
		}
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
			drawKeylolFullImage(dc, block.img, x, y, block.width, block.height)
			y += block.height
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
		case "spoiler":
			drawKeylolSpoiler(dc, bodyFontBytes, block.lines, x, y, contentW, block.height, theme)
			y += block.height
		case "hidden_label":
			drawKeylolHiddenLabel(dc, bodyFontBytes, block.lines, x, y, contentW, block.height)
			y += block.height
		case "currency_table":
			drawKeylolCurrencyTable(dc, fontBytes, block.lines, x, y, contentW, block.height, theme)
			y += block.height
		case "color_red":
			for _, line := range block.lines {
				drawKeylolOutlinedText(dc, bodyFontBytes, 28, color.RGBA{R: 230, G: 70, B: 62, A: 255}, line, float64(x), float64(y))
				y += 40
			}
		case "status_pair":
			drawKeylolStatusPair(dc, bodyFontBytes, block.text, block.desc, x, y)
			y += block.height
		case "color_green":
			for _, line := range block.lines {
				drawKeylolOutlinedText(dc, bodyFontBytes, 28, color.RGBA{R: 54, G: 170, B: 96, A: 255}, line, float64(x), float64(y))
				y += 40
			}
		case "link":
			for _, line := range block.lines {
				drawKeylolLinkLine(dc, bodyFontBytes, 24, line, x, y)
				y += 34
			}
		case "code":
			drawKeylolCodeBlock(dc, bodyFontBytes, block.lines, x, y, contentW, block.height, theme)
			y += block.height
		case "steam_card":
			drawKeylolSteamCard(dc, fontBytes, block, x, y, contentW, block.height)
			y += block.height
		case "video_embed", "video_embed_compact":
			drawW := contentW
			if block.width > 0 {
				drawW = minInt(contentW, block.width)
			}
			drawKeylolVideoCard(dc, fontBytes, block, x, y, drawW, block.height, theme)
			y += block.height
		case "asf_link":
			drawKeylolASFLink(dc, fontBytes, block.title, x, y)
			y += block.height
		case "toolbar":
			for _, line := range block.lines {
				drawInlineEmoji(dc, bodyFontBytes, toolbarSize, theme.Muted, line, float64(x), float64(y))
				y += toolbarLH
			}
		}
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

func keylolBlockGap(prev, cur keylolRenderBlock, base, imageGap int) int {
	gap := base
	if prev.kind == "image" || cur.kind == "image" {
		gap = maxInt(gap, imageGap)
	}
	if strings.HasPrefix(prev.kind, "video_embed") || strings.HasPrefix(cur.kind, "video_embed") || prev.kind == "steam_card" || cur.kind == "steam_card" {
		gap = maxInt(gap, 30)
	}
	if cur.kind == "heading1" || cur.kind == "heading2" {
		gap = maxInt(gap, 34)
	}
	if prev.kind == "asf_link" && (cur.kind == "heading1" || cur.kind == "heading2" || cur.kind == "text") {
		gap = maxInt(gap, 48)
	}
	if strings.HasPrefix(prev.kind, "video_embed") && (cur.kind == "heading1" || cur.kind == "heading2" || cur.kind == "image") {
		gap = maxInt(gap, 56)
	}
	if prev.kind == "spoiler" {
		gap = maxInt(gap, 42)
	}
	if prev.kind == "code" || cur.kind == "code" {
		gap = maxInt(gap, 34)
	}
	return gap
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

func keylolShortVideoDesc(desc string) bool {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return true
	}
	return len([]rune(desc)) <= 20
}

func keylolVideoRenderWidth(kind string, contentW int) int {
	return contentW
}

func keylolCompactVideoCardHeight(contentW int, hasCover bool) int {
	if !hasCover {
		return 290
	}
	innerW := contentW - 64
	coverH := innerW * 9 / 16
	return 34 + 44 + 22 + coverH + 30 + 34
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
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MEDIAPARSER_KEYLOL_THEME")))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(snapshotConfig().KeylolTheme))
	}
	switch mode {
	case "dark", "night", "black":
		return keylolDarkTheme()
	case "light", "day", "white":
		return keylolLightTheme()
	}
	hour := time.Now().Hour()
	if loc, err := time.LoadLocation("Asia/Shanghai"); err == nil {
		hour = time.Now().In(loc).Hour()
	}
	if hour >= 18 || hour < 6 {
		return keylolDarkTheme()
	}
	return keylolLightTheme()
}

func keylolDarkTheme() keylolCardTheme {
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

func keylolLightTheme() keylolCardTheme {
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

func keylolThemeDark(theme keylolCardTheme) bool {
	return int(theme.BG.R)+int(theme.BG.G)+int(theme.BG.B) < 180
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

func drawKeylolBadge(dc *gg.Context, fontBytes []byte, label string, x, y float64) {
	label = firstNonEmpty(strings.TrimSpace(label), "Keylol")
	mustFont(dc, fontBytes, 20)
	tw, _ := dc.MeasureString(label)
	w := 132.0
	if tw+34 > w {
		w = tw + 34
	}
	dc.SetRGB255(74, 137, 218)
	dc.DrawRoundedRectangle(x, y, w, 40, 16)
	dc.Fill()
	drawInlineEmoji(dc, fontBytes, 20, color.RGBA{R: 255, G: 255, B: 255, A: 255}, label, x+17, y+21)
}

func drawKeylolHeaderLogo(dc *gg.Context, fontBytes []byte, x, y float64) {
	if img := keylolOfficialLogo(); img != nil {
		img = trimTransparentImage(img)
		fit := imaging.Fit(img, 198, 64, imaging.Lanczos)
		dc.DrawImage(fit, int(x), int(y))
	} else {
		mustFont(dc, fontBytes, 30)
		dc.SetRGB255(74, 137, 218)
		dc.DrawStringAnchored("Keylol", x, y+28, 0, 0.5)
	}
}

func drawKeylolTopRightLogo(dc *gg.Context, fontBytes []byte, right, cy float64) {
	if img := keylolOfficialLogo(); img != nil {
		img = trimTransparentImage(img)
		fit := imaging.Fit(img, 118, 42, imaging.Lanczos)
		b := fit.Bounds()
		dc.DrawImage(fit, int(right)-b.Dx(), int(cy)-b.Dy()/2)
		return
	}
	mustFont(dc, fontBytes, 29)
	dc.SetRGB255(74, 137, 218)
	dc.DrawStringAnchored("Keylol", right, cy+1, 1, 0.5)
}

func keylolOfficialLogo() image.Image {
	if img := loadPlatformLogo("keylol"); img != nil {
		return img
	}
	return fetchCachedCardImage("keylol-official-logo-v1", "https://keylol.com/template/steamcn_metro/src/img/common/icon_with_text_256h.png", nil)
}

func keylolInlineStatusSuffix(s string) bool {
	s = strings.TrimSpace(s)
	return regexp.MustCompile(`^[\(（]\s*\d+\s*/\s*\d+\s*[\)）]$`).MatchString(s)
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

func drawKeylolSpoiler(dc *gg.Context, fontBytes []byte, lines []string, x, y, w, h int, theme keylolCardTheme) {
	yy := y + 32
	for _, line := range lines {
		mustFont(dc, fontBytes, 23)
		lineW, _ := dc.MeasureString(line)
		pillW := minInt(w, int(lineW)+38)
		bg := color.RGBA{R: 38, G: 46, B: 58, A: 205}
		fg := color.RGBA{R: 226, G: 231, B: 240, A: 255}
		border := color.RGBA{R: 132, G: 146, B: 166, A: 96}
		if !keylolThemeDark(theme) {
			bg = color.RGBA{R: 230, G: 236, B: 244, A: 255}
			fg = color.RGBA{R: 77, G: 86, B: 102, A: 255}
			border = color.RGBA{R: 198, G: 207, B: 220, A: 255}
		}
		dc.SetColor(bg)
		dc.DrawRoundedRectangle(float64(x), float64(yy-24), float64(pillW), 34, 9)
		dc.Fill()
		dc.SetColor(border)
		dc.SetLineWidth(1)
		dc.DrawRoundedRectangle(float64(x)+0.5, float64(yy-24)+0.5, float64(pillW)-1, 33, 9)
		dc.Stroke()
		drawInlineEmoji(dc, fontBytes, 23, fg, line, float64(x+18), float64(yy-7))
		yy += 32
	}
}

func drawKeylolHiddenLabel(dc *gg.Context, fontBytes []byte, lines []string, x, y, w, h int) {
	dc.SetRGBA255(76, 132, 188, 46)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 10)
	dc.Fill()
	dc.SetRGBA255(94, 158, 220, 130)
	dc.SetLineWidth(1)
	dc.DrawRoundedRectangle(float64(x)+0.5, float64(y)+0.5, float64(w)-1, float64(h)-1, 10)
	dc.Stroke()
	yy := y + 28
	for _, line := range lines {
		drawInlineEmoji(dc, fontBytes, 22, color.RGBA{R: 120, G: 188, B: 242, A: 255}, "👁 "+line, float64(x+22), float64(yy))
		yy += 28
	}
}

func keylolCurrencyTableRows(desc string) []string {
	rows := []string{}
	for _, line := range strings.Split(desc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 4 || strings.TrimSpace(parts[0]) == "" {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}

func keylolCurrencyTableHeight(cols int) int {
	if cols <= 0 {
		return 0
	}
	return 214
}

func drawKeylolCurrencyTable(dc *gg.Context, fontBytes []byte, rows []string, x, y, w, h int, theme keylolCardTheme) {
	if len(rows) == 0 {
		return
	}
	tableW := w
	cellW := tableW / len(rows)
	if cellW < 122 {
		cellW = 122
		tableW = cellW * len(rows)
	}
	tableH := h - 10
	headerH := 76
	dc.SetRGB255(48, 48, 50)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(tableW), float64(tableH), 8)
	dc.Fill()
	dc.SetRGBA255(255, 255, 255, 110)
	dc.SetLineWidth(1)
	for i := 1; i < len(rows); i++ {
		xx := float64(x + i*cellW)
		dc.DrawLine(xx, float64(y), xx, float64(y+tableH))
		dc.Stroke()
	}
	dc.DrawLine(float64(x), float64(y+headerH), float64(x+tableW), float64(y+headerH))
	dc.Stroke()
	for i, row := range rows {
		parts := strings.Split(row, "\t")
		if len(parts) < 4 {
			continue
		}
		code := strings.TrimSpace(parts[0])
		flagURL := strings.TrimSpace(parts[1])
		price := strings.TrimSpace(parts[2])
		cny := strings.TrimSpace(parts[3])
		left := x + i*cellW
		cx := float64(left + cellW/2)
		drawInlineEmoji(dc, fontBytes, 29, color.RGBA{R: 255, G: 255, B: 255, A: 255}, code, float64(left+22), float64(y+47))
		if flag := keylolPrepareImage(fetchCardImage(flagURL, nil)); flag != nil && !isBlankCardImage(flag) {
			dc.DrawImage(imaging.Fit(flag, 38, 26, imaging.Lanczos), left+74, y+24)
		}
		drawInlineEmoji(dc, fontBytes, 31, color.RGBA{R: 255, G: 255, B: 255, A: 255}, price, cx-38, float64(y+headerH+54))
		drawInlineEmoji(dc, fontBytes, 28, color.RGBA{R: 255, G: 255, B: 255, A: 255}, cny, cx-64, float64(y+headerH+100))
	}
	setRGB(dc, theme.Line)
	dc.SetLineWidth(1.5)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(tableW), float64(tableH), 8)
	dc.Stroke()
}

func drawKeylolCodeBlock(dc *gg.Context, fontBytes []byte, lines []string, x, y, w, h int, theme keylolCardTheme) {
	dark := keylolThemeDark(theme)
	bg := color.RGBA{R: 10, G: 18, B: 30, A: 255}
	border := color.RGBA{R: 64, G: 84, B: 112, A: 180}
	fg := color.RGBA{R: 201, G: 213, B: 226, A: 255}
	if !dark {
		bg = color.RGBA{R: 243, G: 246, B: 250, A: 255}
		border = color.RGBA{R: 205, G: 215, B: 228, A: 255}
		fg = color.RGBA{R: 70, G: 79, B: 94, A: 255}
	}
	dc.SetColor(bg)
	dc.DrawRoundedRectangle(float64(x), float64(y), float64(w), float64(h), 10)
	dc.Fill()
	dc.SetColor(border)
	dc.SetLineWidth(1)
	dc.DrawRoundedRectangle(float64(x)+0.5, float64(y)+0.5, float64(w)-1, float64(h)-1, 10)
	dc.Stroke()
	yy := y + 30
	for _, line := range lines {
		drawInlineEmoji(dc, fontBytes, 22, fg, line, float64(x+24), float64(yy))
		yy += 32
	}
}

func drawKeylolOutlinedText(dc *gg.Context, fontBytes []byte, size float64, c color.Color, s string, x, y float64) {
	outline := color.RGBA{R: 255, G: 255, B: 255, A: 155}
	for _, off := range [][2]float64{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		drawInlineEmoji(dc, fontBytes, size, outline, s, x+off[0], y+off[1])
	}
	drawInlineEmoji(dc, fontBytes, size, c, s, x, y)
}

func drawKeylolStatusPair(dc *gg.Context, fontBytes []byte, status, count string, x, y int) {
	status = strings.TrimSpace(status)
	count = strings.TrimSpace(count)
	red := color.RGBA{R: 230, G: 70, B: 62, A: 255}
	blue := color.RGBA{R: 71, G: 151, B: 218, A: 255}
	drawKeylolOutlinedText(dc, fontBytes, 28, red, status, float64(x), float64(y))
	dcMeasure := gg.NewContext(10, 10)
	mustFont(dcMeasure, fontBytes, 28)
	tw, _ := dcMeasure.MeasureString(status)
	drawInlineEmoji(dc, fontBytes, 28, blue, count, float64(x)+tw+12, float64(y))
}

func drawKeylolLinkLine(dc *gg.Context, fontBytes []byte, size float64, s string, x, y int) {
	c := color.RGBA{R: 71, G: 151, B: 218, A: 255}
	drawInlineEmoji(dc, fontBytes, size, c, s, float64(x), float64(y))
}

func drawDottedLine(dc *gg.Context, x1, y, x2 float64, c color.Color) {
	dc.SetColor(c)
	dc.SetLineWidth(2)
	for x := x1; x < x2; x += 10 {
		end := x + 5
		if end > x2 {
			end = x2
		}
		dc.DrawLine(x, y, end, y)
		dc.Stroke()
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

	coverW := keylolSteamCardCoverW
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
		dc.SetRGB255(13, 22, 34)
		dc.Fill()
		fit := imaging.Fit(block.img, coverW, coverH, imaging.Lanczos)
		dc.DrawImage(fit, coverX+(coverW-fit.Bounds().Dx())/2, coverY+(coverH-fit.Bounds().Dy())/2)
	}
	dc.ResetClip()

	textX := x + coverW + 36
	title := truncate(firstNonEmpty(block.title, "Steam 游戏"), 54)
	drawInlineEmoji(dc, fontBytes, 25, color.RGBA{R: 237, G: 244, B: 255, A: 255}, "Steam 上的 "+title, float64(textX), float64(y+42))
	yy := y + 78
	for _, line := range block.lines {
		drawInlineEmoji(dc, keylolBodyFontBytes(fontBytes), 20, color.RGBA{R: 166, G: 188, B: 214, A: 255}, line, float64(textX), float64(yy))
		yy += 28
	}
	mustFont(dc, fontBytes, 19)
	dc.SetRGB255(95, 177, 235)
	dc.DrawStringAnchored("在 Steam 上查看 →", float64(textX), float64(y+h-28), 0, 0.5)
}

const keylolSteamCardCoverW = 350

func drawKeylolASFLink(dc *gg.Context, fontBytes []byte, appID string, x, y int) {
	drawInlineEmoji(dc, fontBytes, 27, color.RGBA{R: 53, G: 177, B: 246, A: 255}, "🎮 Steam "+strings.TrimSpace(appID)+" →", float64(x), float64(y+28))
}

func drawKeylolVideoCard(dc *gg.Context, fontBytes []byte, block keylolRenderBlock, x, y, w, h int, theme keylolCardTheme) {
	if block.kind == "video_embed_compact" {
		drawKeylolNativeVideoCard(dc, fontBytes, block, x, y, w, h)
		return
	}
	fx, fy := float64(x), float64(y)
	dark := true
	for i := 8; i >= 1; i-- {
		dc.SetRGBA255(12, 20, 32, 7+i*3)
		dc.DrawRoundedRectangle(fx, fy+float64(i), float64(w), float64(h), 16)
		dc.Fill()
	}
	if dark {
		dc.SetRGB255(20, 34, 54)
	} else {
		dc.SetRGB255(245, 249, 255)
	}
	dc.DrawRoundedRectangle(fx, fy, float64(w), float64(h), 16)
	dc.Fill()
	dc.SetRGBA255(92, 136, 184, 70)
	dc.SetLineWidth(1)
	dc.DrawRoundedRectangle(fx+0.5, fy+0.5, float64(w)-1, float64(h)-1, 16)
	dc.Stroke()

	coverW := keylolVideoCardCoverW
	coverH := h - 28
	coverX := x + 16
	coverY := y + 14
	dc.DrawRoundedRectangle(float64(coverX), float64(coverY), float64(coverW), float64(coverH), 10)
	dc.ClipPreserve()
	if block.img == nil {
		if dark {
			dc.SetRGB255(31, 45, 65)
		} else {
			dc.SetRGB255(221, 234, 248)
		}
		dc.Fill()
		mustFont(dc, fontBytes, 26)
		if dark {
			dc.SetRGB255(145, 178, 214)
		} else {
			dc.SetRGB255(72, 126, 186)
		}
		dc.DrawStringAnchored("Bilibili", float64(coverX+coverW/2), float64(coverY+coverH/2+9), 0.5, 0.5)
	} else {
		if dark {
			dc.SetRGB255(13, 22, 34)
		} else {
			dc.SetRGB255(232, 239, 248)
		}
		dc.Fill()
		fit := imaging.Fit(block.img, coverW, coverH, imaging.Lanczos)
		dc.DrawImage(fit, coverX+(coverW-fit.Bounds().Dx())/2, coverY+(coverH-fit.Bounds().Dy())/2)
	}
	dc.ResetClip()
	drawPlayOverlay(dc, float64(coverX+coverW/2), float64(coverY+coverH/2))

	textX := x + coverW + 36
	title := truncate(firstNonEmpty(block.title, "Bilibili 视频"), 54)
	titleColor := color.RGBA{R: 28, G: 49, B: 72, A: 255}
	bodyColor := color.RGBA{R: 78, G: 98, B: 122, A: 255}
	if dark {
		titleColor = color.RGBA{R: 237, G: 244, B: 255, A: 255}
		bodyColor = color.RGBA{R: 166, G: 188, B: 214, A: 255}
	}
	titleY := y + 42
	drawInlineEmoji(dc, fontBytes, 25, titleColor, title, float64(textX), float64(titleY))
	yy := y + 78
	for _, line := range block.lines {
		drawInlineEmoji(dc, keylolBodyFontBytes(fontBytes), 20, bodyColor, line, float64(textX), float64(yy))
		yy += 28
	}
	mustFont(dc, fontBytes, 19)
	dc.SetRGB255(58, 166, 230)
	linkY := y + h - 28
	dc.DrawStringAnchored("在 Bilibili 查看 →", float64(textX), float64(linkY), 0, 0.5)
}

const keylolVideoCardCoverW = 350

func drawKeylolNativeVideoCard(dc *gg.Context, fontBytes []byte, block keylolRenderBlock, x, y, w, h int) {
	fx, fy := float64(x), float64(y)
	for i := 10; i >= 1; i-- {
		dc.SetRGBA255(0, 0, 0, 5+i*3)
		dc.DrawRoundedRectangle(fx, fy+float64(i), float64(w), float64(h), 18)
		dc.Fill()
	}
	dc.SetRGB255(16, 30, 50)
	dc.DrawRoundedRectangle(fx, fy, float64(w), float64(h), 18)
	dc.FillPreserve()
	dc.SetRGBA255(79, 145, 202, 95)
	dc.SetLineWidth(1.5)
	dc.Stroke()

	title := firstNonEmpty(block.title, "Bilibili 视频")
	titleLines := wrapDisplayTextByPixels(fontBytes, 31, title, float64(w-64), 2)
	yy := float64(y + 42)
	for _, line := range titleLines {
		drawInlineEmoji(dc, fontBytes, 31, color.RGBA{R: 236, G: 244, B: 255, A: 255}, line, float64(x+32), yy)
		yy += 42
	}
	coverX := x + 32
	coverY := int(yy) + 18
	coverW := w - 64
	coverH := coverW * 9 / 16
	if block.img == nil {
		coverH = minInt(coverH, 150)
	}
	dc.DrawRoundedRectangle(float64(coverX), float64(coverY), float64(coverW), float64(coverH), 12)
	dc.ClipPreserve()
	if block.img == nil {
		dc.SetRGB255(29, 47, 70)
		dc.Fill()
		drawBilibiliTransparentLogo(dc, fontBytes, float64(coverX+coverW/2+96), float64(coverY+coverH/2), 0.72)
	} else {
		cover := imaging.Fill(block.img, coverW, coverH, imaging.Center, imaging.Lanczos)
		dc.DrawImage(cover, coverX, coverY)
	}
	dc.ResetClip()
	drawPlayOverlay(dc, float64(coverX+coverW/2), float64(coverY+coverH/2))
	dc.SetRGBA255(255, 255, 255, 70)
	dc.SetLineWidth(2)
	dc.DrawRoundedRectangle(float64(coverX)+1, float64(coverY)+1, float64(coverW)-2, float64(coverH)-2, 12)
	dc.Stroke()

	mustFont(dc, fontBytes, 22)
	dc.SetRGB255(58, 166, 230)
	dc.DrawStringAnchored("在 Bilibili 查看 →", float64(x+32), float64(coverY+coverH+42), 0, 0.5)
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
		for _, token := range wrapTokens(para) {
			next := line + token
			if ww, _ := dc.MeasureString(next); ww > maxW && line != "" {
				out = append(out, strings.TrimSpace(line))
				line = strings.TrimLeft(token, " ")
			} else {
				line = next
			}
			for {
				if ww, _ := dc.MeasureString(line); ww <= maxW || len([]rune(line)) <= 1 {
					break
				}
				rs := []rune(line)
				cut := len(rs) - 1
				for cut > 1 {
					if ww, _ := dc.MeasureString(string(rs[:cut])); ww <= maxW {
						break
					}
					cut--
				}
				out = append(out, strings.TrimSpace(string(rs[:cut])))
				line = strings.TrimLeft(string(rs[cut:]), " ")
			}
		}
		if line != "" {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return fixWrappedLinePunctuation(out)
}

func wrapTokens(s string) []string {
	rs := []rune(s)
	out := []string{}
	for i := 0; i < len(rs); {
		r := rs[i]
		if r == ' ' || r == '\t' {
			for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
				i++
			}
			out = append(out, " ")
			continue
		}
		if isLatinTokenRune(r) {
			start := i
			for i < len(rs) && isLatinTokenRune(rs[i]) {
				i++
			}
			out = append(out, string(rs[start:i]))
			continue
		}
		out = append(out, string(r))
		i++
	}
	return out
}

func isLatinTokenRune(r rune) bool {
	if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
		return true
	}
	switch r {
	case '\'', '.', ',', ':', ';', '!', '?', '-', '_', '/', '+', '&', '®', '™':
		return true
	}
	return false
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
var cardImageCache sync.Map

func fetchCachedCardImage(key, raw string, headers map[string]string) image.Image {
	if v, ok := cardImageCache.Load(key); ok {
		img, _ := v.(image.Image)
		return img
	}
	img := fetchCardImage(raw, headers)
	cardImageCache.Store(key, img)
	return img
}

func renderDefaultPlatformLogoImage(platform string) (image.Image, error) {
	fontBytes, err := file.GetLazyData(text.GlowSansFontFile, control.Md5File, true)
	if err != nil {
		return nil, err
	}
	dc := gg.NewContext(238, 88)
	dc.SetRGBA255(0, 0, 0, 0)
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

func drawTopicLineWithColors(dc *gg.Context, fontBytes []byte, size float64, s string, x, y float64, body, topic color.Color) {
	topicRE := regexp.MustCompile(`#([^#\s]+(?:\[话题\])?)#`)
	matches := topicRE.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		drawInlineEmoji(dc, fontBytes, size, body, s, x, y)
		return
	}
	cursor := x
	last := 0
	for _, m := range matches {
		if m[0] > last {
			cursor += drawInlineEmoji(dc, fontBytes, size, body, s[last:m[0]], cursor, y)
		}
		cursor += drawInlineEmoji(dc, fontBytes, size, topic, s[m[0]:m[1]], cursor, y)
		last = m[1]
	}
	if last < len(s) {
		drawInlineEmoji(dc, fontBytes, size, body, s[last:], cursor, y)
	}
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
	if platform == "bilibili" {
		drawBilibiliTransparentLogo(dc, fontBytes, right, cy, 1)
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

func drawBilibiliTransparentLogo(dc *gg.Context, fontBytes []byte, right, cy float64, scale float64) {
	if scale <= 0 {
		scale = 1
	}
	iconW, iconH := 70.0*scale, 52.0*scale
	x := right - 206*scale
	y := cy - iconH/2
	dc.SetRGB255(0, 174, 236)
	dc.SetLineWidth(5 * scale)
	dc.DrawRoundedRectangle(x, y, iconW, iconH, 9*scale)
	dc.Stroke()
	dc.SetLineWidth(4 * scale)
	dc.DrawLine(x+18*scale, y-8*scale, x+28*scale, y+6*scale)
	dc.DrawLine(x+52*scale, y-8*scale, x+42*scale, y+6*scale)
	dc.Stroke()
	dc.DrawCircle(x+24*scale, y+28*scale, 3.8*scale)
	dc.Fill()
	dc.DrawCircle(x+46*scale, y+28*scale, 3.8*scale)
	dc.Fill()
	mustFont(dc, fontBytes, 40*scale)
	dc.SetRGBA255(255, 255, 255, 90)
	dc.DrawStringAnchored("bilibili", right+2*scale, cy+3*scale, 1, 0.5)
	dc.SetRGB255(0, 174, 236)
	dc.DrawStringAnchored("bilibili", right, cy+1*scale, 1, 0.5)
}

func drawCustomPlatformLogoBadge(dc *gg.Context, platform string, right, cy float64) bool {
	if platform == "kuaishou" {
		return false
	}
	logoPlatform := platform
	if platform == "xiaoheihe" {
		logoPlatform = "xiaoheihe_light"
		if keylolThemeDark(keylolCardThemeNow()) {
			logoPlatform = "xiaoheihe_dark"
		}
	}
	img := loadPlatformLogo(logoPlatform)
	if img == nil {
		return false
	}
	if !logoHasTransparency(img) {
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
	if platform == "xianyu" {
		w, h = 96.0, 96.0
	}
	x, y := right-w, cy-h/2
	fit := imaging.Fit(img, int(w)-pad*2, int(h)-pad*2, imaging.Lanczos)
	b := fit.Bounds()
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
	opaque := 0
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
			opaque++
			rr, gg, bb := int(r>>8), int(g>>8), int(bl>>8)
			if rr < 242 || gg < 242 || bb < 242 {
				visible++
			}
		}
	}
	if sampled == 0 {
		return false
	}
	opaqueRatio := float64(opaque) / float64(sampled)
	if opaqueRatio > 0.001 && opaqueRatio < 0.95 && logoHasTransparency(img) {
		return true
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

func trimTransparentImage(img image.Image) image.Image {
	if img == nil || !logoHasTransparency(img) {
		return img
	}
	b := img.Bounds()
	minX, minY := b.Max.X, b.Max.Y
	maxX, maxY := b.Min.X, b.Min.Y
	found := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a>>8 < 16 {
				continue
			}
			if x < minX {
				minX = x
			}
			if y < minY {
				minY = y
			}
			if x+1 > maxX {
				maxX = x + 1
			}
			if y+1 > maxY {
				maxY = y + 1
			}
			found = true
		}
	}
	if !found || minX >= maxX || minY >= maxY {
		return img
	}
	crop := image.Rect(minX, minY, maxX, maxY)
	if crop.Dx() == b.Dx() && crop.Dy() == b.Dy() {
		return img
	}
	return imaging.Crop(img, crop)
}

func drawWhiteLogoBadge(dc *gg.Context, fontBytes []byte, platform string, right, cy float64) bool {
	if platform == "bilibili" {
		drawBilibiliTransparentLogo(dc, fontBytes, right, cy, 1)
		return true
	}
	dark := keylolThemeDark(keylolCardThemeNow())
	w, h := 238.0, 88.0
	x, y := right-w, cy-h/2
	switch platform {
	case "xiaohongshu":
		mustFont(dc, fontBytes, 46)
		dc.SetRGB255(255, 36, 66)
		dc.DrawStringAnchored("小红书", right-4, cy+2, 1, 0.5)
	case "xiaoheihe":
		drawHeyboxIcon(dc, x+10, y+17)
		mustFont(dc, fontBytes, 30)
		if dark {
			dc.SetRGB255(238, 241, 246)
		} else {
			dc.SetRGB255(28, 28, 32)
		}
		dc.DrawStringAnchored("小黑盒", right-4, cy-10, 1, 0.5)
		mustFont(dc, fontBytes, 15)
		dc.DrawStringAnchored("HEYBOX", right-8, cy+24, 1, 0.5)
	case "twitter":
		mustFont(dc, fontBytes, 68)
		dc.SetRGB255(5, 5, 5)
		dc.DrawStringAnchored("𝕏", right-10, cy+1, 1, 0.5)
	case "kuaishou":
		drawKuaishouMark(dc, x+8, y+10)
		mustFont(dc, fontBytes, 40)
		if dark {
			dc.SetRGB255(255, 120, 48)
		} else {
			dc.SetRGB255(64, 64, 64)
		}
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
	if keylolThemeDark(keylolCardThemeNow()) {
		dc.SetRGB255(238, 241, 246)
	} else {
		dc.SetRGB255(28, 28, 32)
	}
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
	drawPlayOverlay(dc, cx, cy)
}

func drawPlayOverlay(dc *gg.Context, cx, cy float64) {
	dc.SetRGBA255(30, 34, 40, 92)
	dc.DrawRoundedRectangle(cx-58, cy-58, 116, 116, 16)
	dc.Fill()
	dc.SetRGBA255(185, 190, 198, 155)
	dc.DrawRegularPolygon(3, cx+13, cy, 46, gg.Radians(90))
	dc.Fill()
}

func drawFloatingCoverCell(dc *gg.Context, img image.Image, x, y, w, h int, showPlay bool) {
	drawFloatingImageCellAnchored(dc, img, x, y, w, h, imaging.Center)
	if !showPlay {
		return
	}
	cx := float64(x + w/2)
	cy := float64(y + h/2)
	drawPlayOverlay(dc, cx, cy)
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

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	out := values[0]
	for _, v := range values[1:] {
		if v < out {
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
