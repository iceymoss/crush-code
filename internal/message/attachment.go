package message

import (
	"slices"
	"strings"
)

// Attachment 附件
// 定义了附件的结构,例如,文件路径、文件名、
type Attachment struct {
	FilePath string // 文件路径
	FileName string // 文件名
	MimeType string // MIME类型(文本、图片、音频、视频等)
	Content  []byte // 内容
}

// IsText 判断附件是否为文本类型
func (a Attachment) IsText() bool {
	return strings.HasPrefix(a.MimeType, "text/")
}

// IsImage 判断附件是否为图片类型
func (a Attachment) IsImage() bool {
	return strings.HasPrefix(a.MimeType, "image/")
}

// ContainsTextAttachment 判断附件列表中是否包含文本类型附件
// 返回true如果附件列表中包含文本类型附件，否则返回false
func ContainsTextAttachment(attachments []Attachment) bool {
	return slices.ContainsFunc(attachments, func(a Attachment) bool {
		return a.IsText()
	})
}
