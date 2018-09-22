package main

import "github.com/zhwei820/httpclient"

func main() {
	for ii := 0; ii < 100; ii++ {
		go work()
	}
	select {}
}

func work() {
	res, err := httpclient.Get("http://192.168.1.5:5000/", nil, nil, 3)
	if err != nil {
		println("err", err.Error())
		return
	}
	data, err := res.String()
	println(data)
}
