package llm

import "testing"

func TestDecodeJSONAcceptsBareArrayForSingleListStruct(t *testing.T) {
	var one struct {
		Facts []struct {
			Text string `json:"text"`
		} `json:"facts"`
	}
	if err := decodeJSON(`[{"text":"a"},{"text":"b"}]`, &one); err != nil || len(one.Facts) != 2 {
		t.Fatalf("bare array: %+v err=%v", one, err)
	}
	var two struct {
		A []string `json:"a"`
		B []string `json:"b"`
	}
	if err := decodeJSON(`["x"]`, &two); err == nil {
		t.Fatal("two list fields make a bare array ambiguous")
	}
	var obj struct {
		Facts []string `json:"facts"`
	}
	if err := decodeJSON(`{"facts":["k"]}`, &obj); err != nil || len(obj.Facts) != 1 {
		t.Fatalf("normal object: %+v err=%v", obj, err)
	}
}
