package main

import (
	"reflect"
	"testing"
)

func TestParseLetters(t *testing.T) {
	got, err := parseLetters("d,a,a,c")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"d", "a", "a", "c"}) {
		t.Fatalf("got %v", got)
	}
	if _, err := parseLetters("a,中文"); err == nil {
		t.Fatal("expected invalid letter")
	}
}
