package hello

func Greet(name string) string {
	if name == "" {
		name = "world"
	}
	return "Hello, " + name + "! Accorda OSS is ready."
}
