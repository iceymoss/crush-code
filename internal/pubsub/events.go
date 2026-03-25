package pubsub

import "context"

const (
	// CreatedEvent 定义了创建事件
	CreatedEvent EventType = "created"
	// UpdatedEvent 定义了更新事件
	UpdatedEvent EventType = "updated"
	// DeletedEvent 定义了删除事件
	DeletedEvent EventType = "deleted"
)

// Subscriber 定义了订阅者的接口, 使用了泛型，例如，Session、Message、Permission等都可以作为参数
// T 是泛型，表示事件的类型
// Subscribe 方法返回一个通道，用于接收事件
// context.Context 是上下文，用于取消订阅
// <-chan Event[T] 是事件通道，用于接收事件
type Subscriber[T any] interface {
	Subscribe(context.Context) <-chan Event[T]
}

// EventType 定义了事件类型, 例如，CreatedEvent、UpdatedEvent、DeletedEvent等
type EventType string

// Event 定义了事件的结构
// Type 是事件类型
// Payload 是事件的负载，泛型T表示事件的类型, 例如，Session、Message、Permission等都可以作为参数
type Event[T any] struct {
	Type    EventType // 事件类型
	Payload T         // 事件的负载
}

// Publisher 定义了发布者的接口
// T 是泛型，表示事件的类型
// Publish 方法用于发布事件, 参数是事件类型和事件的负载
type Publisher[T any] interface {
	Publish(EventType, T) // 发布事件
}

// 什么是发布者：发布者是事件的发布者，用于发布事件
// 什么是订阅者：订阅者是事件的订阅者，用于订阅事件
// 什么是事件：事件是事件的发布者发布的消息
