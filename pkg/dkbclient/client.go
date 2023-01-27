package dkbclient

import (
	"encoding/json"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	httpClient *http.Client
}

type AccountOverview struct {
	AccountName     string
	Account         string
	Date            string
	Amount          string
	AccountType     string
	TransactionLink string
}

func New() Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	httpClient := &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		fmt.Println("redirect")
		fmt.Println(req.URL)
		return nil
	}}

	return Client{httpClient: httpClient}
}

func (c *Client) Login(username, password string) error {

	resp, err := c.httpClient.Get("https://www.dkb.de/banking")
	if err != nil {
		panic(err)
	}
	d, err := goquery.NewDocumentFromReader(resp.Body)

	sid, _ := d.Find("form#login input[name='$sID$']").Attr("value")
	token, _ := d.Find("form#login input[name='token']").Attr("value")

	data := url.Values{}

	data.Add("$sID$", sid)
	data.Add("token", token)
	data.Add("j_username", username)
	data.Add("j_password", password)

	resp, err = c.httpClient.PostForm("https://www.dkb.de/banking", data)
	if err != nil {
		return err
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return err
	}

	xsrfToken, _ := doc.Find("input[name='XSRFPreventionToken']").Attr("value")
	action, _ := doc.Find("form#confirmForm").Attr("action")
	err = c.pollVerification(action, xsrfToken)
	if err != nil {
		return err
	}
	return nil
}

func (c *Client) pollVerification(confirmFormAction string, xsrfToken string) error {

	pollId := time.Now().UTC().UnixMilli() * 1000
	pollUrl := "https://www.dkb.de" + confirmFormAction

	for i := 0; i < 60; i++ {

		pollId += 1

		fullPollUrl := pollUrl + "?$event=pollingVerification&$ignore.request=true&_=" + strconv.Itoa(int(pollId))

		resp, _ := c.httpClient.Get(fullPollUrl)

		bytes, _ := io.ReadAll(resp.Body)

		bodyJson := make(map[string]string)
		err := json.Unmarshal(bytes, &bodyJson)
		if err != nil {
			return err
		}

		if bodyJson["state"] == "PROCESSED" {
			fmt.Println("success")
			break
		}

		err = resp.Body.Close()
		if err != nil {
			return err
		}

		time.Sleep(3000 * time.Millisecond)
	}

	postData := url.Values{}
	postData.Add("$event", "next")
	postData.Add("XSRFPreventionToken", xsrfToken)

	resp, err := c.httpClient.PostForm(pollUrl, postData)
	if err != nil {
		return err
	}

	fmt.Println(resp.Status)

	return nil
}

func (c *Client) ParseOverview() ([]AccountOverview, error) {
	resp, err := c.httpClient.Get("https://www.dkb.de/DkbTransactionBanking/content/banking/financialstatus/FinancialComposite/FinancialStatus.xhtml?$event=init")
	if err != nil {
		return nil, err
	}

	d, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return nil, err
	}

	rows := d.Find("tr.mainRow")

	var result []AccountOverview

	rows.Each(func(i int, row *goquery.Selection) {
		var (
			accountName     string
			account         string
			date            string
			amount          string
			accountType     string
			transactionLink string
		)
		cols := row.Find("td")
		cols.Each(func(i int, col *goquery.Selection) {
			switch i {
			case 0:
				accountName = strings.TrimSpace(col.Find("div.forceWrap").Text())
				break
			case 1:
				account = strings.TrimSpace(col.Find("div.iban").Text())
				break
			case 2:
				date = strings.TrimSpace(col.Text())
				break
			case 3:
				amount = strings.TrimSpace(col.Text())
				break
			case 4:
				link := col.Find("a.evt-paymentTransaction")
				if len(link.Nodes) > 0 {
					if strings.HasPrefix(account, "DE") {
						accountType = "Account"
					} else {
						accountType = "credit card"
					}

					transactionLink, _ = link.Attr("href")
				} else {
					accountType = "depot"
					transactionLink, _ = col.Find("a.evt-depot").Attr("href")
				}
				break
			default:
			}
		})
		result = append(result, AccountOverview{
			AccountName:     accountName,
			Account:         account,
			Date:            date,
			Amount:          amount,
			AccountType:     accountType,
			TransactionLink: transactionLink,
		})
	})
	return result, nil
}

func (c *Client) GetAccountTransactions(a AccountOverview, from time.Time, to time.Time) (string, error) {
	resp, err := c.httpClient.Get("https://www.dkb.de" + a.TransactionLink)
	if err != nil {
		return "", err
	}

	d, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return "", err
	}
	accountNumber, _ := d.Find("select#id-1615473160_slAllAccounts option[selected='selected']").Attr("value")

	postUrl := "https://www.dkb.de/banking/finanzstatus/kontoumsaetze"
	postData := url.Values{}
	postData.Add("slTransactionStatus", "0")
	postData.Add("slSearchPeriod", "1")
	postData.Add("searchPeriodRadio", "1")
	postData.Add("transactionDate", from.Format("02.01.2006"))
	postData.Add("toTransactionDate", to.Format("02.01.2006"))
	postData.Add("$event", "search")
	postData.Add("slAllAccounts", accountNumber)

	fmt.Println(postData)

	resp, err = c.httpClient.PostForm(postUrl, postData)

	if err != nil {
		return "", err
	}

	resp, err = c.httpClient.Get("https://www.dkb.de/banking/finanzstatus/kontoumsaetze?$event=csvExport")

	result, err := io.ReadAll(resp.Body)

	if err != nil {
		return "", err
	}

	return string(result), nil
}
