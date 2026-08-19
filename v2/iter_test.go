package apexjson

import "testing"

func TestIterators(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	data := []byte(`{"users":[{"id":1,"name":"alice"},{"id":2,"name":"bob"}],"ok":true}`)
	if err := dec.Parse(data); err != nil {
		t.Fatal(err)
	}

	root := dec.Root()
	var object ObjectIter
	object.Reset(root)
	if !object.Next() || object.Key() != "users" || object.Value().Type() != TypeArray {
		t.Fatalf("first object field = %q/%v", object.Key(), object.Value().Type())
	}
	if !object.Next() || object.Key() != "ok" || !object.Value().Bool() {
		t.Fatalf("second object field = %q/%v", object.Key(), object.Value().Type())
	}
	if object.Next() {
		t.Fatal("object iterator should be exhausted")
	}

	arrayValue := root.get("users")
	var array ArrayIter
	array.Reset(arrayValue)
	wantNames := []string{"alice", "bob"}
	for i := 0; array.Next(); i++ {
		if array.Index() != i {
			t.Fatalf("array index = %d, want %d", array.Index(), i)
		}
		row := array.Value()
		var fields ObjectIter
		fields.Reset(row)
		var gotID int64
		var gotName string
		for fields.Next() {
			switch fields.Key() {
			case "id":
				gotID = fields.Value().Int()
			case "name":
				gotName = fields.Value().Str()
			}
		}
		if gotID != int64(i+1) || gotName != wantNames[i] {
			t.Fatalf("row %d = %d/%q", i, gotID, gotName)
		}
	}
	if array.Index() != -1 {
		t.Fatalf("array index after exhaustion = %d, want -1", array.Index())
	}
}

func TestIteratorsZeroAlloc(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	data := []byte(`{"users":[{"id":1,"name":"alice"},{"id":2,"name":"bob"}]}`)
	if err := dec.Parse(data); err != nil {
		t.Fatal(err)
	}
	root := dec.Root()
	users := root.get("users")
	var array ArrayIter
	var object ObjectIter
	allocs := testing.AllocsPerRun(1000, func() {
		array.Reset(users)
		for array.Next() {
			object.Reset(array.Value())
			for object.Next() {
				_ = object.KeyBytes()
				_ = object.Value().Type()
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("iterator allocated %v/op, want 0", allocs)
	}
}
