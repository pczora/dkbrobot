package main

import (
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"time"
)

func main() {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}

	client := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		fmt.Println("redirect")
		fmt.Println(req.URL)
		return nil
	}}

	resp, err := client.Get("https://www.dkb.de/banking")
	if err != nil {
		panic(err)
	}

	d, err := goquery.NewDocumentFromReader(resp.Body)

	sid, _ := d.Find("form#login input[name='$sID$']").Attr("value")
	token, _ := d.Find("form#login input[name='token']").Attr("value")

	fmt.Println(sid)
	fmt.Println(token)
	data := url.Values{}
	data.Add("$sID$", sid)
	data.Add("token", token)
	data.Add("j_username", "")
	data.Add("j_password", "")

	resp, err = client.PostForm("https://www.dkb.de/banking", data)
	if err != nil {
		panic(err)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		panic(err)
	}

	//token, _ := doc.Find("input[name='XSRFPreventionToken']").Attr("value")
	action, _ := doc.Find("form#confirmForm").Attr("action")
	//loginConfirmed := false

	pollId := time.Now().UTC().UnixMilli() * 1000
	pollUrl := "https://www.dkb.de" + action
	for i := 0; i < 60; i++ {
		pollId += 1
		url := pollUrl + "?$event=pollingVerification&$ignore.request=true&_=" + strconv.Itoa(int(pollId))
		fmt.Printf("polling: %v\n", url)
		resp, _ = client.Get(url)
		fmt.Print(resp.Status)
		bytes, _ := io.ReadAll(resp.Body)

		fmt.Println(string(bytes))
		time.Sleep(3000 * time.Millisecond)
	}

}
