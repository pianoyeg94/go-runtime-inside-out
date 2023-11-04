package main

import (
	"fmt"
	"log"
	"net/http"
)

func handler(w http.ResponseWriter, req *http.Request) {
	if _, err := fmt.Fprint(w, "<html><body><h1>Hello, World!</h1></body></html>"); err != nil {
		log.Println(err)
	}
}

func main() {
	http.HandleFunc("/", handler)

	if err := http.ListenAndServe(":8085", nil); err != nil {
		panic(err)
	}
}
