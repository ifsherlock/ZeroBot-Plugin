package deerpipe

import (
	"bytes"
	_ "embed"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/FloatTech/floatbox/file"
	"github.com/FloatTech/gg"
	"github.com/FloatTech/ttl"
	"github.com/FloatTech/zbputils/control"
	"github.com/FloatTech/zbputils/img/text"
	"github.com/disintegration/imaging"
	"golang.org/x/image/font"
)

//go:embed assets/deerpipe.png
var deerPipePNG []byte

//go:embed assets/check.png
var checkPNG []byte

//go:embed assets/akkarin.png
var akkarinPNG []byte

const (
	cellW = 100
	cellH = 100
)

var (
	assetOnce   sync.Once
	deerPipeImg image.Image
	checkImg    image.Image
	akkarinImg  image.Image
	deerIcon25  image.Image
	deerIcon50  image.Image

	fontOnce    sync.Once
	fontData    []byte
	boldData    []byte
	fontErr     error
	faceMu      sync.Mutex
	faceCache   = map[string]font.Face{}
	avatarCache = ttl.NewCache[int64, image.Image](24 * time.Hour)
	avatarHTTP  = &http.Client{Timeout: 8 * time.Second}
)

func loadAssets() {
	assetOnce.Do(func() {
		deerPipeImg = mustDecodePNG(deerPipePNG)
		checkImg = mustDecodePNG(checkPNG)
		akkarinImg = mustDecodePNG(akkarinPNG)
		// 用🦌管图代替标题里的 🦌 emoji（普通字体没有该字形）
		deerIcon25 = imaging.Resize(deerPipeImg, 0, 25, imaging.Lanczos)
		deerIcon50 = imaging.Resize(deerPipeImg, 0, 50, imaging.Lanczos)
	})
}

func mustDecodePNG(data []byte) image.Image {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	return img
}

func ensureFonts() error {
	fontOnce.Do(func() {
		fontData, fontErr = file.GetLazyData(text.FontFile, control.Md5File, true)
		if fontErr != nil {
			return
		}
		boldData, fontErr = file.GetLazyData(text.BoldFontFile, control.Md5File, true)
	})
	return fontErr
}

func faceFor(bold bool, size float64) (font.Face, error) {
	if err := ensureFonts(); err != nil {
		return nil, err
	}
	key := strconv.FormatFloat(size, 'f', 1, 64)
	data := fontData
	if bold {
		key = "b" + key
		data = boldData
	}
	faceMu.Lock()
	defer faceMu.Unlock()
	if face, ok := faceCache[key]; ok {
		return face, nil
	}
	face, err := gg.ParseFontFace(data, size)
	if err != nil {
		return nil, err
	}
	faceCache[key] = face
	return face, nil
}

func setFace(dc *gg.Context, bold bool, size float64) error {
	face, err := faceFor(bold, size)
	if err != nil {
		return err
	}
	dc.SetFontFace(face)
	return nil
}

// drawTopLeft 模拟 PIL draw.text 的左上角定位。
func drawTopLeft(dc *gg.Context, s string, x, y float64) {
	_, h := dc.MeasureString(s)
	dc.DrawStringAnchored(s, x, y+h*0.82, 0, 0)
}

// titlePart 标题片段：文本或内嵌图片（代替 🦌 emoji）。
type titlePart struct {
	text string
	img  image.Image
}

func measureParts(dc *gg.Context, parts []titlePart) float64 {
	w := 0.0
	for _, p := range parts {
		if p.img != nil {
			w += float64(p.img.Bounds().Dx())
			continue
		}
		pw, _ := dc.MeasureString(p.text)
		w += pw
	}
	return w
}

func drawParts(dc *gg.Context, parts []titlePart, x, y, size float64) {
	cx := x
	for _, p := range parts {
		if p.img != nil {
			ih := float64(p.img.Bounds().Dy())
			dc.DrawImage(p.img, int(cx), int(y+(size-ih)/2))
			cx += float64(p.img.Bounds().Dx())
			continue
		}
		drawTopLeft(dc, p.text, cx, y)
		pw, _ := dc.MeasureString(p.text)
		cx += pw
	}
}

// monthCalendar 与 Python calendar.monthcalendar 一致：周一为一周第一天，空位为 0。
func monthCalendar(year int, month time.Month) [][7]int {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.Local)
	days := first.AddDate(0, 1, -1).Day()
	weekday := (int(first.Weekday()) + 6) % 7
	weeks := make([][7]int, 0, 6)
	var week [7]int
	idx := weekday
	for day := 1; day <= days; day++ {
		week[idx] = day
		idx++
		if idx == 7 {
			weeks = append(weeks, week)
			week = [7]int{}
			idx = 0
		}
	}
	if idx > 0 {
		weeks = append(weeks, week)
	}
	return weeks
}

// renderCalendar 生成 🦌签到日历，布局与原插件 gen_calendar 一致。
func renderCalendar(now time.Time, records map[int]int, name string, avatar image.Image) ([]byte, error) {
	loadAssets()
	weeks := monthCalendar(now.Year(), now.Month())
	width, height := 700, cellH*(len(weeks)+1)
	dc := gg.NewContext(width, height)
	dc.SetRGB(1, 1, 1)
	dc.Clear()

	if avatar == nil {
		avatar = akkarinImg
	}
	dc.DrawImage(imaging.Resize(avatar, 80, 80, imaging.Lanczos), 10, 10)

	if err := setFace(dc, false, 25); err != nil {
		return nil, err
	}
	dc.SetRGB(0, 0, 0)
	title := []titlePart{
		{text: now.Format("2006-01") + " "},
		{img: deerIcon25},
		{text: "签到日历"},
	}
	drawParts(dc, title, 100, 10, 25)
	drawTopLeft(dc, "@"+name, 100, 40)

	for weekIdx, week := range weeks {
		for dayIdx, day := range week {
			if day == 0 {
				continue
			}
			x0 := dayIdx * cellW
			y0 := (weekIdx + 1) * cellH
			dc.DrawImage(deerPipeImg, x0, y0)
			if err := setFace(dc, false, 25); err != nil {
				return nil, err
			}
			dc.SetRGB(0, 0, 0)
			drawTopLeft(dc, strconv.Itoa(day), float64(x0)+5, float64(y0)+cellH-35)
			count, checked := records[day]
			if !checked {
				continue
			}
			dc.DrawImage(checkImg, x0, y0)
			if count > 1 {
				label := "x" + strconv.Itoa(count)
				if count > 999 {
					label = "x999+"
				}
				if err := setFace(dc, true, 20); err != nil {
					return nil, err
				}
				dc.SetRGB(0.86, 0.15, 0.15)
				lw, _ := dc.MeasureString(label)
				drawTopLeft(dc, label, float64(x0)+cellW-lw-5, float64(y0)+cellH-25)
			}
		}
	}
	return encodePNG(dc.Image())
}

type rankRow struct {
	Name   string
	Avatar image.Image
	Count  int
}

// renderRank 生成 🦌榜，布局与原插件 gen_rank 一致。
func renderRank(rows []rankRow) ([]byte, error) {
	loadAssets()
	width, height := 400, (len(rows)+1)*100
	dc := gg.NewContext(width, height)
	dc.SetRGB(1, 1, 1)
	dc.Clear()

	if err := setFace(dc, true, 50); err != nil {
		return nil, err
	}
	dc.SetRGB(0.86, 0.15, 0.15)
	title := []titlePart{
		{text: "本月Top5"},
		{img: deerIcon50},
		{text: "榜"},
	}
	tw := measureParts(dc, title)
	drawParts(dc, title, (float64(width)-tw)/2, 25, 50)

	for idx, row := range rows {
		y := (idx + 1) * 100
		avatar := row.Avatar
		if avatar == nil {
			avatar = akkarinImg
		}
		dc.DrawImage(imaging.Resize(avatar, 80, 80, imaging.Lanczos), 10, y+10)
		if err := setFace(dc, false, 25); err != nil {
			return nil, err
		}
		dc.SetRGB(0, 0, 0)
		drawTopLeft(dc, "@"+row.Name, 100, float64(y)+10)
		if err := setFace(dc, true, 25); err != nil {
			return nil, err
		}
		dc.SetRGB(0.86, 0.15, 0.15)
		drawTopLeft(dc, "x"+strconv.Itoa(row.Count), 100, float64(y)+50)
	}
	return encodePNG(dc.Image())
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	if buf.Len() == 0 {
		return nil, errors.New("empty image")
	}
	return buf.Bytes(), nil
}

// fetchAvatar 拉取 QQ 头像，带 1 天缓存；失败返回 nil 走默认头像。
func fetchAvatar(uid int64) image.Image {
	if uid <= 0 {
		return nil
	}
	if img := avatarCache.Get(uid); img != nil {
		return img
	}
	resp, err := avatarHTTP.Get("https://q4.qlogo.cn/g?b=qq&nk=" + strconv.FormatInt(uid, 10) + "&s=100")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	img, _, err := image.Decode(resp.Body)
	if err != nil {
		return nil
	}
	avatarCache.Set(uid, img)
	return img
}
