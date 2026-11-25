package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"git.shino.boo/devilreef/cadence/server/internal/agent"
	"git.shino.boo/devilreef/cadence/server/internal/commands"
	"git.shino.boo/devilreef/cadence/server/internal/devices"
)

// btText is the tool's fixed spoken text in one language (v1's BLUETOOTH_TEXT): ru is casual «ты»,
// gender-neutral, and holds no command word the speaker would act on in its own reply
type btText struct {
	ru        bool
	wakeFirst string
}

var (
	btEN = btText{wakeFirst: "To pair, wake me first."}
	btRU = btText{ru: true, wakeFirst: "Чтобы подключить, сначала позови меня."}
)

// bluetoothTexts is the turn's device's language, english when it is unknown
func bluetoothTexts(ctx context.Context, d BluetoothDeps, deviceID string) btText {
	if d.Settings == nil {
		return btEN
	}
	v, err := d.Settings(ctx, deviceID)
	if base, _, _ := strings.Cut(strings.ToLower(v.Language), "-"); err == nil && base == "ru" {
		return btRU
	}
	return btEN
}

// pairing is pair_phone's confirmation: how long the window stays open and the name the phone sees
func (t btText) pairing(seconds int, name string) string {
	if t.ru {
		if name == "" {
			name = "колонку"
		}
		return "Блютус включён на " + ruSpan(seconds) + ": выбери " + name + " в телефоне или компьютере."
	}
	if name == "" {
		name = "the speaker"
	}
	return "Bluetooth is on for " + enSpan(seconds) + ": pick " + name + " on your phone or computer."
}

var (
	enMinutes = map[int]string{1: "a minute", 2: "two minutes", 3: "three minutes", 4: "four minutes", 5: "five minutes"}
	ruMinutes = map[int]string{1: "минуту", 2: "две минуты", 3: "три минуты", 4: "четыре минуты", 5: "пять минут"}
)

func enSpan(s int) string {
	if m, ok := enMinutes[s/60]; ok && s%60 == 0 {
		return m
	}
	return fmt.Sprintf("%d seconds", s)
}

func ruSpan(s int) string {
	if m, ok := ruMinutes[s/60]; ok && s%60 == 0 {
		return m
	}
	word := "секунд"
	switch n := s % 100; {
	case n >= 11 && n <= 14:
	case n%10 == 1:
		word = "секунду"
	case n%10 >= 2 && n%10 <= 4:
		word = "секунды"
	}
	return fmt.Sprintf("%d %s", s, word)
}

// bluetoothError is the model's view of a failed command: never device text.
func bluetoothError(err error, retryable bool) *agent.ToolError {
	var device *commands.DeviceError
	if errors.As(err, &device) {
		switch device.Code {
		case "wake_required":
			return agent.WakeRequired()
		case "unknown_device":
			return &agent.ToolError{
				Code: "not_found", Message: "That device knows no Bluetooth device with that id.",
				Hint: "Use list (or scan) for the ids.", Retryable: true,
			}
		case "connect_failed":
			return &agent.ToolError{
				Code: "connect_failed", Message: "The Bluetooth device did not connect.",
				Hint: "Tell the user to check that it is on and near the speaker.",
			}
		case "receive_off":
			return &agent.ToolError{
				Code: "receive_off", Message: "Receiving over Bluetooth is turned off on that device.",
				Hint: "Tell the user the owner turns it on in the device's Bluetooth settings.",
			}
		case "unavailable":
			return unavailable("Bluetooth is not available on that device right now.")
		}
	}
	switch {
	case errors.Is(err, devices.ErrNotFound), errors.Is(err, devices.ErrUnknownCapability), errors.Is(err, devices.ErrUnknownCommand):
		return &agent.ToolError{Code: "no_capability", Message: "That device cannot do this over Bluetooth."}
	case errors.Is(err, commands.ErrOffline):
		return &agent.ToolError{Code: "device_offline", Message: "The speaker is offline."}
	case errors.Is(err, commands.ErrTimeout):
		return &agent.ToolError{Code: "timeout", Message: "The speaker did not answer.", Retryable: retryable}
	}
	return btFailed()
}

func btFailed() *agent.ToolError {
	return &agent.ToolError{Code: "bluetooth_failed", Message: "The speaker did not do it."}
}
