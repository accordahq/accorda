package hello

import "testing"

func TestGreet(t *testing.T) {
	got := Greet("friend")
	want := "Hello, friend! Accorda OSS is ready."
	if got != want {
		t.Fatalf("Greet() = %q, want %q", got, want)
	}
}
