package memory_test

import (
	"testing"

	"git.shino.boo/devilreef/cadence/server/internal/memory"
)

func TestLooksSecret(t *testing.T) {
	secret := []string{
		"The wifi password is hunter2",
		"wifi_password: sunflower",
		"Пароль от wifi: qwerty",
		"пароль 12345678",
		"Пароль — кот123",
		"Hue API key = abcd-efgh",
		"API-key: xyz",
		"apikey is in the drawer: 77AA",
		"токен бота это 123abc",
		"Ключ от сейфа: 4471",
		"token=abc",
		"Use sk-proj-AbCdEf1234567890 for OpenAI",
		"ghp_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789",
		"github_pat_11ABCDEFG0123456789_abcdefghijklmnop",
		"glpat-AbCdEfGhIjKlMnOpQrSt",
		"AKIAIOSFODNN7EXAMPLE",
		"xoxb-1234-5678-abcdefgh",
		"AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.sig",
		"the hub key 5f2b8c9e1a3d4f6b7c8e9a0b1c2d3e4f",
		"token Xk9mPq2RtY7wZ3nB5vC8dF1gH4",
		"its id is 3f2b8c9e-1a3d-4f6b-7c8e-9a0b1c2d3e4f",
		"wJalrXUtnFEMI/K7MDENG/bPxRfiCYzq81kd93Kl",
		"Mail the landlord at john.doe@example.com",
		"Call mom on +996 555 123 456",
		"Номер мамы 0555123456",
		"office (555) 123-4567",
		"Call 0555.123.456 after six",
		// dotted digits that are no IPv4 address: an octet past 255, or a group beside the four
		"Call 8.999.123.45.67 after six",
		"Номер 999.555.123.45",
		"Call 192.168.100.200.5 tonight",
		"Call 5.192.168.1.1 tonight",
	}
	for _, s := range secret {
		if !memory.LooksSecret(s) {
			t.Errorf("missed: %q", s)
		}
	}
	plain := []string{
		"Wake me at 7:30 on weekdays and keep the volume at 40",
		"Большой свет — это light.living_room_main",
		"Big light means light.living_room_ceiling_lamp_group",
		"Battery sensor is sensor.outdoor_temperature_sensor_battery_level_2",
		"Lives in Bishkek, born in 1990, has 2 cats",
		"Born on 1990-05-14",
		"Ключи от машины лежат на полке в прихожей",
		"Weather failed with an invalid token error; the web search worked",
		"Spotify token expired; logging in again fixed it",
		"Password login works better than token auth",
		"Token-based login failed, the cookie one worked",
		"Prefers SceneLivingRoomMovieNight for movies",
		"Room 12 is the kids' room with 3 beds",
		"Volume steps 10, 20, 30, 40 and 50 sound fine",
		"x86_64-linux-gnu-toolchain-v2 is on the server",
		"The secret recipe is grandma's",
		"Любит джаз и тишину по утрам",
		"Use the @home scene in the evening",
		"The speaker is at 192.168.10.200",
		"Home Assistant runs on 10.0.0.12:8123",
		"The router is 192.168.1.1.",
		"Hosts 10.0.0.2 and 10.0.0.3 are the speakers",
		"Firmware 255.255.255.0 netmask",
	}
	for _, s := range plain {
		if memory.LooksSecret(s) {
			t.Errorf("false positive: %q", s)
		}
	}
}
