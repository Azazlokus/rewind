package protocol

import "testing"

// FuzzDecode утверждает единственный жёсткий инвариант декодера: он никогда не
// паникует, каким бы кривым ни был ввод. Падение здесь — это баг типа
// отказ-в-обслуживании, поэтому этот фаззер защищает весь путь приёма. Бинарный
// кодек итерации 3 сохраняет эту цель; меняется только сид-корпус.
func FuzzDecode(f *testing.F) {
	seeds := [][]byte{
		nil,
		{byte(MsgInput), 1, 0, 0, 0, 2, 3, 0}, // валидный Input
		{byte(MsgJoin), 6, 'p', 'l', 'a', 'y', 'e', 'r'}, // валидный Join
		{byte(MsgJoin), 17},      // имя слишком длинное
		{byte(MsgJoin), 5, 'a'},  // обрезанное имя
		{0xff, 0x01, 0x02, 0x03}, // неизвестный тип + мусор
		{byte(MsgInput)},         // обрезанный Input
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Не должно паниковать. Ошибочный результат намеренно игнорируется.
		msg, err := DecodeClient(data)
		if err != nil {
			return
		}
		// Сообщение, которое декодировалось, обязано пережить обратное
		// кодирование без ошибки — успешный decode не может дать то, что мы не
		// можем сериализовать.
		switch msg.Type {
		case MsgInput:
			if _, err := AppendInput(nil, msg.Input); err != nil {
				t.Fatalf("re-encode input %+v: %v", msg.Input, err)
			}
		case MsgJoin:
			if _, err := AppendJoin(nil, msg.Join); err != nil {
				t.Fatalf("re-encode join %+v: %v", msg.Join, err)
			}
		default:
			t.Fatalf("decoded unexpected type %v from %q", msg.Type, data)
		}
	})
}
