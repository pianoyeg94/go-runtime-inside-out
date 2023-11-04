package main

import (
	"fmt"
	"log"
	"net/http"
	"runtime"
)

func init() {
	runtime.GOMAXPROCS(1)
}

func handler(w http.ResponseWriter, req *http.Request) {
	if _, err := fmt.Fprint(w, "<html><body><h1>Hello, World!</h1></body></html>"); err != nil {
		log.Println(err)
	}
}

func main() {
	http.HandleFunc("/", handler)

	if err := http.ListenAndServe(":8086", nil); err != nil {
		panic(err)
	}
}
