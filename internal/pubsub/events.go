package pubsub

import "context"

const (
	CreatedEvent EventType = "created"
	UpdatedEvent EventType = "updated"
	DeletedEvent EventType = "deleted"
)

// Suscriber 定义订阅事件的接口, 需要由订阅者实现
type Suscriber[T any] interface {
	// Subscribe 返回一个订阅通道，后续订阅者可以通过向订阅通道发送事件
	Subscribe(context.Context) <-chan Event[T]
}

// Publisher 发送事件
type Publisher[T any] interface {
	Publish(EventType, T)
}

// Event 定义事件结构体
type Event[T any] struct {
	Type    EventType
	Payload T
}

// EventType 枚举
type EventType string
