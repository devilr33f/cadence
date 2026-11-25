package subscriptions

import (
	"context"
	"errors"

	"git.shino.boo/devilreef/cadence/server/internal/settings"
)

// Request is a subscription to create: Delivery "" takes the device's
// subscriptionDelivery setting, Until "" is until unsubscribed, Events none
// every event the module emits.
type Request struct {
	Topic     string
	Events    []string
	Delivery  string
	Until     string
	Device    string
	CreatedBy string
}

// ErrDevice is a device the settings do not know.
var ErrDevice = errors.New("subscriptions: unknown device")

// Subscribe creates the device's subscription to a topic, replacing its one
// of the same topic: ErrTopic for a topic that does not parse or that its
// module refuses, ErrUnknownModule, ErrEvent, ErrInstance, ErrDevice,
// ErrInvalid for a delivery or until it does not know.
func (h *Hub) Subscribe(ctx context.Context, r Request) (Subscription, error) {
	t, err := ParseTopic(r.Topic)
	if err != nil {
		return Subscription{}, ErrTopic
	}
	evs, err := h.d.Registry.check(t, r.Events)
	if err != nil {
		return Subscription{}, err
	}
	if r.Delivery == "" {
		v, err := h.d.Settings(ctx, r.Device)
		switch {
		case errors.Is(err, settings.ErrNotFound):
			return Subscription{}, ErrDevice
		case err != nil:
			return Subscription{}, err
		}
		r.Delivery = v.SubscriptionDelivery
		if r.Delivery == "" {
			r.Delivery = DeliverySpeaker
		}
	}
	if r.Until == "" {
		r.Until = UntilUnsubscribed
	}
	switch {
	case r.Delivery != DeliverySpeaker && r.Delivery != DeliveryAgent && r.Delivery != DeliveryMute,
		r.Until != UntilUnsubscribed && r.Until != UntilNextStop,
		r.CreatedBy != ByVoice && r.CreatedBy != ByAPI:
		return Subscription{}, ErrInvalid
	}
	s, err := h.d.Store.Create(ctx, Subscription{
		Topic: t.String(), Events: evs, Delivery: r.Delivery, Device: r.Device, Until: r.Until, CreatedBy: r.CreatedBy,
	})
	if errors.Is(err, ErrInvalid) {
		// the device's foreign key: a device deleted meanwhile
		return Subscription{}, ErrDevice
	}
	if err != nil {
		return Subscription{}, err
	}
	h.d.Log.Info("subscription_created", "id", s.ID, "module", t.Module, "device_id", s.Device, "delivery", s.Delivery, "by", s.CreatedBy)
	h.publish(s.Device, EventChangedSub, map[string]any{"id": s.ID, "topic": s.Topic, "action": "created"})
	h.reconcile()
	return s, nil
}

// List is a device's subscriptions, every device's for "".
func (h *Hub) List(ctx context.Context, deviceID string) ([]Subscription, error) {
	return h.d.Store.List(ctx, deviceID)
}

// Unsubscribe deletes a subscription by id, the device's alone unless
// deviceID is "".
func (h *Hub) Unsubscribe(ctx context.Context, id, deviceID string) (Subscription, error) {
	s, err := h.d.Store.Get(ctx, id)
	if err != nil {
		return Subscription{}, err
	}
	if deviceID != "" && s.Device != deviceID {
		return Subscription{}, ErrNotFound
	}
	if err := h.d.Store.Delete(ctx, id, deviceID); err != nil {
		return Subscription{}, err
	}
	h.d.Log.Info("subscription_deleted", "id", s.ID, "device_id", s.Device)
	h.publish(s.Device, EventChangedSub, map[string]any{"id": s.ID, "topic": s.Topic, "action": "deleted"})
	return s, nil
}

// UnsubscribeTopic deletes the device's subscription of a topic.
func (h *Hub) UnsubscribeTopic(ctx context.Context, deviceID, topic string) (Subscription, error) {
	t, err := ParseTopic(topic)
	if err != nil {
		return Subscription{}, ErrNotFound
	}
	subs, err := h.d.Store.List(ctx, deviceID)
	if err != nil {
		return Subscription{}, err
	}
	for _, s := range subs {
		if s.Topic == t.String() {
			return h.Unsubscribe(ctx, s.ID, deviceID)
		}
	}
	return Subscription{}, ErrNotFound
}

// Topics lists what can be followed under a prefix <module>:<instance>[:path...]
// (a trailing wildcard is the same prefix): the module's List.
func (h *Hub) Topics(ctx context.Context, prefix string) ([]Entry, error) {
	t, err := parsePrefix(prefix)
	if err != nil {
		return nil, err
	}
	m, ok := h.d.Registry.Get(t.Module)
	if !ok {
		return nil, ErrUnknownModule
	}
	if err := m.Validate(t.Instance, t.Path); err != nil {
		return nil, ErrTopic
	}
	out, err := m.List(ctx, t.Instance, t.Path)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}

// parsePrefix reads a topic prefix: a module and an instance at least.
func parsePrefix(s string) (Topic, error) {
	t, err := ParseTopic(s)
	if err == nil {
		return t, nil
	}
	// <module>:<instance> alone
	return ParseTopic(s + ":" + Wildcard)
}
