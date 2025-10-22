package event

import (
	"time"
)

// appStartTime app开始时间
var appStartTime time.Time

func AppInitialized() {
	appStartTime = time.Now()

	// 发送事件, 通知app初始化完成
	send("app initialized")
}

func AppExited() {
	// 发送事件, 统计app运行时间
	duration := time.Since(appStartTime).Truncate(time.Second)
	send(
		"app exited",
		"app duration pretty", duration.String(),
		"app duration in seconds", int64(duration.Seconds()),
	)
	Flush()
}

func SessionCreated() {
	send("session created")
}

func SessionDeleted() {
	send("session deleted")
}

func SessionSwitched() {
	send("session switched")
}

func FilePickerOpened() {
	send("filepicker opened")
}

func PromptSent(props ...any) {
	send(
		"prompt sent",
		props...,
	)
}

func PromptResponded(props ...any) {
	send(
		"prompt responded",
		props...,
	)
}

func TokensUsed(props ...any) {
	send(
		"tokens used",
		props...,
	)
}
