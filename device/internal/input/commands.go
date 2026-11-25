package input

// CommandWords is cadenced's own command vocabulary (Ruling M2-R6), never vendor material: a board with a command
// model sends it in audiod's Configure, and a CommandWord's id is the index into it, which CommandPolicy gets.
// The stock command model (version …stations-shrd-bt-likes, in the firmware's stock root) spots these phrases; a
// phrase it can say that is missing here is dropped by audiod, and one it cannot say never fires — a phrase with no
// device action is still listed, so its hit is counted and left to the server.
func CommandWords() []string {
	return []string{
		"стоп", "хватит", "пауза",
		"тише", "громче",
		"дальше", "следующий", "назад", "продолжи", "играй",
		"выше", "ниже", "вправо", "влево", "вверх", "вниз", "домой",
		"включи свет", "выключи свет", "включи телевизор", "выключи телевизор",
		"включи блютуз", "выключи блютуз", "лайк", "дизлайк",
	}
}
