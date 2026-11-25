package enroll

type Board struct{ ID, Name string }

// Boards are the models a device can be created and enrolled as; each id is the one its board package reports
// (device/boards/<id>), since enrolling claims the code only for a device whose board matches the model
var Boards = []Board{
	{"sim", "Simulator"},
	{"yandex-pickle", "Yandex pickle"},
	{"yandex-midi", "Yandex midi"},
	{"yandex-micro", "Yandex Micro (experimental)"},
	{"vk-capsula-mini", "VK Capsula Mini"},
	{"sim-tv", "Screen simulator"},
	{"rpi", "Raspberry Pi"},
	{"android-tv", "Android TV"},
}

func knownModel(id string) bool {
	for _, b := range Boards {
		if b.ID == id {
			return true
		}
	}
	return false
}
