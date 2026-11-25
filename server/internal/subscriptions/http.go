package subscriptions

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"git.shino.boo/devilreef/cadence/server/internal/api"
)

const (
	tag = "subscriptions"
	// noStore keeps outside text out of every cache (rule 8)
	noStore      = "no-store"
	maxInboxPage = 200
)

// CreateSubscriptionRequest is a subscription to create on a device.
type CreateSubscriptionRequest struct {
	Topic    string   `json:"topic" minLength:"3" maxLength:"512" doc:"<module>:<instance>:<path...>, segments percent-encoded, * only as the last segment"`
	Events   []string `json:"events,omitempty" maxItems:"6" uniqueItems:"true" enum:"stopped,question,approval,error,done,changed" doc:"the events to deliver; absent: every event the module emits"`
	Delivery string   `json:"delivery,omitempty" enum:"speaker,agent,mute" doc:"absent: the device's subscriptionDelivery setting"`
	Until    string   `json:"until,omitempty" enum:"unsubscribed,next_stop" doc:"absent: unsubscribed"`
	Device   string   `json:"device" format:"uuid"`
}

// SubscriptionReplyRequest answers an open question, with evidence dashboard.
type SubscriptionReplyRequest struct {
	Device   string   `json:"device,omitempty" format:"uuid" doc:"the device whose inbox holds the question; absent: any"`
	Token    string   `json:"token" minLength:"1" maxLength:"64" pattern:"^[A-Za-z0-9_-]+$" doc:"the question's token"`
	Answers  []string `json:"answers,omitempty" maxItems:"16" doc:"option ids of a clarify question"`
	Message  string   `json:"message,omitempty" maxLength:"2000" doc:"a free answer to a clarify question"`
	Decision string   `json:"decision,omitempty" enum:"once,session,deny" doc:"an approval's answer"`
}

// SubscriptionList is a list of subscriptions.
type SubscriptionList struct {
	Subscriptions []Subscription `json:"subscriptions" nullable:"false"`
}

// SubscriptionTopicList is what a module lists under a prefix.
type SubscriptionTopicList struct {
	Entries []Entry `json:"entries" nullable:"false"`
}

// SubscriptionInbox is a device's inbox: pending updates, unread ones and open
// questions, oldest first. Their texts are the outside systems' own.
type SubscriptionInbox struct {
	Items []InboxItem `json:"items" nullable:"false"`
}

// SubscriptionReplyResult is a question answered.
type SubscriptionReplyResult struct {
	InboxItem InboxItem `json:"item"`
}

type listIn struct {
	Device string `query:"device" format:"uuid" doc:"one device's; absent: every device's"`
}

type createIn struct{ Body CreateSubscriptionRequest }

type idIn struct {
	ID string `path:"id" format:"uuid"`
}

type topicsIn struct {
	Prefix string `query:"prefix" required:"true" minLength:"3" maxLength:"512" doc:"<module>:<instance>[:path...]"`
}

type inboxIn struct {
	Device string `query:"device" required:"true" format:"uuid"`
}

type replyIn struct{ Body SubscriptionReplyRequest }

type listOut struct{ Body SubscriptionList }

type subOut struct{ Body Subscription }

type topicsOut struct{ Body SubscriptionTopicList }

type inboxOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         SubscriptionInbox
}

type replyOut struct {
	CacheControl string `header:"Cache-Control"`
	Body         SubscriptionReplyResult
}

var docs = map[string]api.Doc{
	"list-subscriptions":       {Summary: "List subscriptions"},
	"create-subscription":      {Summary: "Subscribe a device to a topic, replacing its subscription of that topic"},
	"delete-subscription":      {Summary: "Delete a subscription"},
	"list-subscription-topics": {Summary: "List what can be followed under a topic prefix"},
	"get-subscription-inbox":   {Summary: "Get a device's subscription inbox"},
	"reply-to-subscription":    {Summary: "Answer an open question of a followed topic"},
}

// RegisterAdmin mounts the subscriptions API, admin's alone. The handlers log
// no outside text: a failure logs its operation.
func RegisterAdmin(a huma.API, h *Hub, log *slog.Logger) {
	defer api.Describe(a, docs)
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	failed := func(op string, err error) error {
		if !errors.Is(err, context.Canceled) {
			log.Error("subscription_api_failed", "op", op)
		}
		return api.Error(http.StatusInternalServerError, "internal")
	}
	huma.Register(a, huma.Operation{
		OperationID: "list-subscriptions", Method: http.MethodGet, Path: "/subscriptions", Tags: []string{tag},
	}, func(ctx context.Context, in *listIn) (*listOut, error) {
		subs, err := h.List(ctx, in.Device)
		if err != nil {
			return nil, failed("list", err)
		}
		return &listOut{Body: SubscriptionList{Subscriptions: subs}}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "create-subscription", Method: http.MethodPost, Path: "/subscriptions", Tags: []string{tag},
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIn) (*subOut, error) {
		b := in.Body
		s, err := h.Subscribe(ctx, Request{
			Topic: b.Topic, Events: b.Events, Delivery: b.Delivery, Until: b.Until, Device: b.Device, CreatedBy: ByAPI,
		})
		switch {
		case errors.Is(err, ErrTopic):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_topic", "/body/topic")
		case errors.Is(err, ErrUnknownModule):
			return nil, api.Error(http.StatusUnprocessableEntity, "unknown_module", "/body/topic")
		case errors.Is(err, ErrInstance):
			return nil, api.Error(http.StatusUnprocessableEntity, "unknown_instance", "/body/topic")
		case errors.Is(err, ErrEvent):
			return nil, api.Error(http.StatusUnprocessableEntity, "unsupported_event", "/body/events")
		case errors.Is(err, ErrDevice):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrInvalid):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid")
		case err != nil:
			return nil, failed("create", err)
		}
		return &subOut{Body: s}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "delete-subscription", Method: http.MethodDelete, Path: "/subscriptions/{id}", Tags: []string{tag},
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		_, err := h.Unsubscribe(ctx, in.ID, "")
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, failed("delete", err)
		}
		return nil, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "list-subscription-topics", Method: http.MethodGet, Path: "/subscriptions/topics", Tags: []string{tag},
	}, func(ctx context.Context, in *topicsIn) (*topicsOut, error) {
		entries, err := h.Topics(ctx, in.Prefix)
		switch {
		case errors.Is(err, ErrTopic):
			return nil, api.Error(http.StatusUnprocessableEntity, "invalid_topic", "/query/prefix")
		case errors.Is(err, ErrUnknownModule):
			return nil, api.Error(http.StatusUnprocessableEntity, "unknown_module", "/query/prefix")
		case err != nil:
			if ctx.Err() == nil {
				log.Warn("subscription_api_failed", "op", "topics")
			}
			return nil, api.Error(http.StatusBadGateway, "module_failed")
		}
		return &topicsOut{Body: SubscriptionTopicList{Entries: entries}}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "get-subscription-inbox", Method: http.MethodGet, Path: "/subscriptions/inbox", Tags: []string{tag},
	}, func(ctx context.Context, in *inboxIn) (*inboxOut, error) {
		items, err := h.d.Store.Inbox(ctx, in.Device, maxInboxPage)
		switch {
		case errors.Is(err, ErrNotFound):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case err != nil:
			return nil, failed("inbox", err)
		}
		return &inboxOut{CacheControl: noStore, Body: SubscriptionInbox{Items: items}}, nil
	})
	huma.Register(a, huma.Operation{
		OperationID: "reply-to-subscription", Method: http.MethodPost, Path: "/subscriptions/reply", Tags: []string{tag},
	}, func(ctx context.Context, in *replyIn) (*replyOut, error) {
		b := in.Body
		it, err := h.Reply(ctx, b.Device, Answer{
			Token: b.Token, Answers: b.Answers, Message: b.Message, Decision: b.Decision,
			By: Evidence{Via: ViaDashboard},
		})
		switch {
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrNoAnswer):
			return nil, api.Error(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrAnswerKind):
			return nil, api.Error(http.StatusUnprocessableEntity, "answer_mismatch")
		case errors.Is(err, ErrUnknownModule):
			return nil, api.Error(http.StatusConflict, "module_unavailable")
		case errors.Is(err, ErrAnswerRefused):
			return nil, api.Error(http.StatusBadGateway, "module_failed")
		case err != nil:
			return nil, failed("reply", err)
		}
		return &replyOut{CacheControl: noStore, Body: SubscriptionReplyResult{InboxItem: it}}, nil
	})
}
