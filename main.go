// Package main ZeroBot-Plugin slim runtime for llbot + media parser.
package main

//go:generate go run github.com/FloatTech/ZeroBot-Plugin/abineundo/ref -r .

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "github.com/FloatTech/ZeroBot-Plugin/abineundo"
	"github.com/FloatTech/ZeroBot-Plugin/kanban"
	"github.com/FloatTech/ZeroBot-Plugin/kanban/banner"
	_ "github.com/FloatTech/ZeroBot-Plugin/plugin/dailynews"
	_ "github.com/FloatTech/ZeroBot-Plugin/plugin/deerpipe"
	"github.com/FloatTech/ZeroBot-Plugin/plugin/mediaparser"
	"github.com/FloatTech/floatbox/file"
	"github.com/FloatTech/floatbox/process"
	"github.com/FloatTech/zbputils/control"
	"github.com/sirupsen/logrus"
	zero "github.com/wdvxdr1123/ZeroBot"
	"github.com/wdvxdr1123/ZeroBot/driver"
	"github.com/wdvxdr1123/ZeroBot/message"
)

type zbpcfg struct {
	Z               zero.Config        `json:"zero"`
	W               []*driver.WSClient `json:"ws"`
	S               []*driver.WSServer `json:"wss"`
	ForceBase64File bool               `json:"force_base64_file"`
}

var config zbpcfg

func init() {
	sus := make([]int64, 0, 16)
	d := flag.Bool("d", false, "Enable debug level log and higher.")
	w := flag.Bool("w", false, "Enable warning level log and higher.")
	h := flag.Bool("h", false, "Display this help.")
	token := flag.String("t", "", "Set AccessToken of WSClient.")
	url := flag.String("u", "ws://127.0.0.1:6700", "Set Url of WSClient.")
	adana := flag.String("n", "ZeroBot", "Set default nickname.")
	prefix := flag.String("p", "/", "Set command prefix.")
	runcfg := flag.String("c", "", "Run from config file.")
	save := flag.String("s", "", "Save default config to file and exit.")
	late := flag.Uint("l", 233, "Response latency (ms).")
	rsz := flag.Uint("r", 4096, "Receiving buffer ring size.")
	maxpt := flag.Uint("x", 4, "Max process time (min).")
	markmsg := flag.Bool("m", false, "Don't mark message as read automatically")
	markRead := flag.Bool("mark-message", false, "Mark message as read automatically")
	fb64 := flag.Bool("fb64", false, "Force to send base64 file.")
	webui := flag.String("webui", "0.0.0.0:3000", "Set built-in WebUI listen address, use off to disable.")
	flag.BoolVar(&file.SkipOriginal, "mirror", false, "Use mirrored lazy data at first")

	flag.Parse()
	flagProvided := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		flagProvided[f.Name] = true
	})
	systemSettings := mediaparser.LoadSystemSettings()

	if *h {
		fmt.Println("Usage:")
		flag.PrintDefaults()
		os.Exit(0)
	}
	if *d && !*w {
		logrus.SetLevel(logrus.DebugLevel)
	}
	if *w {
		logrus.SetLevel(logrus.WarnLevel)
	}

	for _, s := range flag.Args() {
		i, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			sus = append(sus, i)
		}
	}
	sus = mergeIDs(sus, systemSettings.SuperUsers)

	if *runcfg != "" {
		f, err := os.Open(*runcfg)
		if err != nil {
			panic(err)
		}
		config.W = make([]*driver.WSClient, 0, 2)
		err = json.NewDecoder(f).Decode(&config)
		f.Close()
		if err != nil {
			panic(err)
		}
		config.Z.Driver = make([]zero.Driver, len(config.W)+len(config.S))
		for i, w := range config.W {
			config.Z.Driver[i] = w
		}
		for i, s := range config.S {
			config.Z.Driver[i+len(config.W)] = s
		}
		logrus.Infoln("[main] loaded config file", *runcfg)
		return
	}

	if !flagProvided["u"] && systemSettings.WSURL != "" {
		*url = systemSettings.WSURL
	}
	if !flagProvided["t"] && systemSettings.WSToken != "" {
		*token = systemSettings.WSToken
	}
	if !flagProvided["n"] && systemSettings.Nickname != "" {
		*adana = systemSettings.Nickname
	}
	if !flagProvided["p"] && systemSettings.CommandPrefix != "" {
		*prefix = systemSettings.CommandPrefix
	}
	if !flagProvided["webui"] && systemSettings.WebUIAddr != "" {
		*webui = systemSettings.WebUIAddr
	}

	config.W = []*driver.WSClient{driver.NewWebSocketClient(*url, *token)}
	config.Z = zero.Config{
		NickName:       append([]string{*adana}, "ATRI", "atri"),
		CommandPrefix:  *prefix,
		SuperUsers:     sus,
		RingLen:        *rsz,
		Latency:        time.Duration(*late) * time.Millisecond,
		MaxProcessTime: time.Duration(*maxpt) * time.Minute,
		MarkMessage:    *markRead && !*markmsg,
		Driver:         []zero.Driver{config.W[0]},
	}
	if qqDriver, ok := mediaparser.NewQQBotDriver(systemSettings); ok {
		config.Z.Driver = append(config.Z.Driver, qqDriver)
		logrus.Infoln("[main] official QQBot driver enabled")
	}
	if tgDriver, ok := mediaparser.NewTelegramBotDriver(systemSettings); ok {
		config.Z.Driver = append(config.Z.Driver, tgDriver)
		logrus.Infoln("[main] Telegram Bot driver enabled")
	}
	config.ForceBase64File = *fb64
	if *webui != "" {
		os.Setenv("ZBP_BUILTIN_WEBUI", *webui)
	}
	mediaparser.SetRuntimeSystemSettings(mediaparser.SystemSettings{
		WebUIAddr:               *webui,
		WSURL:                   *url,
		WSToken:                 *token,
		OneBotDataDir:           firstNonEmptyMain(os.Getenv("ONEBOT_DATA_DIR"), systemSettings.OneBotDataDir),
		Nickname:                *adana,
		CommandPrefix:           *prefix,
		SuperUsers:              sus,
		QQBotEnabled:            systemSettings.QQBotEnabled,
		QQBotName:               systemSettings.QQBotName,
		QQBotAppID:              systemSettings.QQBotAppID,
		QQBotSecret:             systemSettings.QQBotSecret,
		QQBotOpenID:             systemSettings.QQBotOpenID,
		QQBotGroupOpenID:        systemSettings.QQBotGroupOpenID,
		QQBotPublicBase:         systemSettings.QQBotPublicBase,
		QQBotCardDisabled:       systemSettings.QQBotCardDisabled,
		QQBotMediaEnabled:       systemSettings.QQBotMediaEnabled,
		QQBotMarkdown:           systemSettings.QQBotMarkdown,
		TGBotEnabled:            systemSettings.TGBotEnabled,
		TGBotName:               systemSettings.TGBotName,
		TGBotToken:              systemSettings.TGBotToken,
		TGBotAPIBase:            systemSettings.TGBotAPIBase,
		TGBotProxy:              systemSettings.TGBotProxy,
		TGBotMediaEnabled:       systemSettings.TGBotMediaEnabled,
		TGBotSuperUsers:         append([]int64{}, systemSettings.TGBotSuperUsers...),
		TGBotPrivateMode:        systemSettings.TGBotPrivateMode,
		TGBotGroupMode:          systemSettings.TGBotGroupMode,
		TGBotGroupUserMode:      systemSettings.TGBotGroupUserMode,
		TGBotUserWhitelist:      append([]int64{}, systemSettings.TGBotUserWhitelist...),
		TGBotUserBlacklist:      append([]int64{}, systemSettings.TGBotUserBlacklist...),
		TGBotGroupWhitelist:     append([]int64{}, systemSettings.TGBotGroupWhitelist...),
		TGBotGroupBlacklist:     append([]int64{}, systemSettings.TGBotGroupBlacklist...),
		TGBotGroupUserWhitelist: append([]int64{}, systemSettings.TGBotGroupUserWhitelist...),
		TGBotGroupUserBlacklist: append([]int64{}, systemSettings.TGBotGroupUserBlacklist...),
	})

	if *save != "" {
		f, err := os.Create(*save)
		if err != nil {
			panic(err)
		}
		err = json.NewEncoder(f).Encode(&config)
		f.Close()
		if err != nil {
			panic(err)
		}
		logrus.Infoln("[main] config saved to", *save)
		os.Exit(0)
	}
}

func mergeIDs(a, b []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(a)+len(b))
	for _, id := range append(append([]int64{}, a...), b...) {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func firstNonEmptyMain(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func main() {
	if !strings.Contains(runtime.Version(), "go1.2") {
		rand.Seed(time.Now().UnixNano()) //nolint: staticcheck
	}
	message.SetForceBase64File(config.ForceBase64File)

	// 本运行时依赖 WebUI 与各插件自带的黑白名单管控，默认打开全局响应；
	// 否则 control 引擎注册的插件（deerpipe 等）在从未发过“全局响应”的部署上永远静默。
	if !control.CanResponse(0) {
		if err := control.Response(0); err != nil {
			logrus.Warnln("[main] enable global response failed:", err)
		} else {
			logrus.Infoln("[main] global response enabled by default")
		}
	}

	zero.OnFullMatchGroup([]string{"help", "/help", ".help", "菜单"}, zero.OnlyToMe).SetBlock(true).
		Handle(func(ctx *zero.Ctx) {
			ctx.SendChain(message.Text(banner.Banner, "\n发送 /服务列表 查看功能\n发送 /用法 mediaparser 查看媒体解析用法"))
		})
	zero.OnFullMatch("查看zbp公告", zero.OnlyToMe, zero.AdminPermission).SetBlock(true).
		Handle(func(ctx *zero.Ctx) {
			ctx.SendChain(message.Text(strings.ReplaceAll(kanban.Kanban(), "\t", "")))
		})

	zero.RunAndBlock(&config.Z, process.GlobalInitMutex.Unlock)
}
