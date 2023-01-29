package dkbclient

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"github.com/gocarina/gocsv"
	"golang.org/x/text/encoding/charmap"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func init() {
	gocsv.SetCSVReader(func(in io.Reader) gocsv.CSVReader {
		// DKB uses ISO8859-15 (for whatever reason)
		reader := csv.NewReader(charmap.ISO8859_15.NewDecoder().Reader(in))
		reader.Comma = ';'
		return reader
	})
}

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

		b, _ := io.ReadAll(resp.Body)

		bodyJson := make(map[string]string)
		err := json.Unmarshal(b, &bodyJson)
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
						accountType = "account"
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

type DkbDateTime struct {
	time.Time
}

func (date *DkbDateTime) MarshalCSV() (string, error) {
	return date.Time.Format("02.01.2006"), nil
}

func (date *DkbDateTime) UnmarshalCSV(csv string) (err error) {
	t, err := time.Parse("02.01.2006", csv)
	date.Time = t
	return err
}

type DkbAmount struct {
	float64
}

func (amount *DkbAmount) MarshalCSV() (string, error) {
	return strconv.FormatFloat(amount.float64, 'f', 2, 64), nil
}

func (amount *DkbAmount) UnmarshalCSV(csv string) (err error) {
	normalizedAmount := amount.normalizeAmount(csv)
	floatAmount, err := strconv.ParseFloat(normalizedAmount, 64)
	if err != nil {
		return err
	}
	amount.float64 = floatAmount
	return nil
}

func (amount *DkbAmount) normalizeAmount(a string) string {
	result := strings.Replace(a, ".", "", -1)
	result = strings.Replace(result, ",", ".", -1)
	return result
}

type AccountTransaction struct {
	Date              DkbDateTime `csv:"Buchungstag"`
	ValueDate         DkbDateTime `csv:"Wertstellung"`
	PostingText       string      `csv:"Buchungstext"`
	Payee             string      `csv:"Auftraggeber / Begünstigter"`
	Purpose           string      `csv:"Verwendungszweck"`
	BankAccountNumber string      `csv:"Kontonummer"`
	BankCode          string      `csv:"Bankleitzahl"`
	Amount            DkbAmount   `csv:"Betrag (EUR)"`
	CreditorID        string      `csv:"Gläubiger-ID"`
	MandateReference  string      `csv:"Mandatsreferenz"`
	CustomerReference string      `csv:"Kundenreferenz"`
}

type CreditCardTransaction struct {
	Marked    string      `csv:"Umsatz abgerechnet aber nicht im Saldo enthalten"` // Ignored (for now)
	ValueDate DkbDateTime `csv:"Wertstellung"`
	Date      DkbDateTime `csv:"Belegdatum"`
	Purpose   string      `csv:"Beschreibung"`
	Amount    DkbAmount   `csv:"Betrag (EUR)"`
	//OriginalAmount DkbAmount   `csv:"Ursprünglicher Betrag"` // Ignored (for now)
}

func (c *Client) GetAccountTransactions(a AccountOverview, from time.Time, to time.Time) ([]AccountTransaction, error) {
	result, err := c.getTransactions(a, from, to)
	if err != nil {
		return nil, err
	}

	return c.parseAccountTransactions(result)
}

func (c *Client) GetCreditCardTransactions(a AccountOverview, from time.Time, to time.Time) ([]CreditCardTransaction, error) {
	result, err := c.getTransactions(a, from, to)
	if err != nil {
		return nil, err
	}

	return c.parseCreditCardTransactions(result)
}

func (c *Client) getTransactions(a AccountOverview, from time.Time, to time.Time) ([]byte, error) {
	resp, err := c.httpClient.Get("https://www.dkb.de" + a.TransactionLink)
	if err != nil {
		return nil, err
	}

	d, err := goquery.NewDocumentFromReader(resp.Body)

	if err != nil {
		return nil, err
	}
	accountNumber, _ := d.Find("select[name='slAllAccounts'] option[selected='selected']").Attr("value")

	postUrl := "https://www.dkb.de/banking/finanzstatus/kontoumsaetze"
	postData := url.Values{}
	postData.Add("slTransactionStatus", "0")
	postData.Add("slSearchPeriod", "1")
	postData.Add("searchPeriodRadio", "1")
	postData.Add("transactionDate", from.Format("02.01.2006"))
	postData.Add("toTransactionDate", to.Format("02.01.2006"))
	postData.Add("$event", "search")
	postData.Add("slAllAccounts", accountNumber)

	resp, err = c.httpClient.PostForm(postUrl, postData)

	if err != nil {
		return nil, err
	}

	resp, err = c.httpClient.Get("https://www.dkb.de/banking/finanzstatus/kontoumsaetze?$event=csvExport")

	result, err := io.ReadAll(resp.Body)

	if err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) parseAccountTransactions(transactions []byte) ([]AccountTransaction, error) {
	var result []AccountTransaction

	r := bufio.NewReader(bytes.NewReader(transactions))
	skipLines(r, 6) // First six lines consist of metadata

	err := gocsv.Unmarshal(r, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (c *Client) parseCreditCardTransactions(transactions []byte) ([]CreditCardTransaction, error) {
	var result []CreditCardTransaction

	r := bufio.NewReader(bytes.NewReader(transactions))
	skipLines(r, 6) // First six lines consist of metadata

	err := gocsv.Unmarshal(r, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func skipLines(r *bufio.Reader, n int) {
	for i := 0; i < n; i++ {
		r.ReadLine()
	}
}
