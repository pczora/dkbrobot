package dkbclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

// Client is an HTTP client for the DKB web interface
type Client struct {
	httpClient  *http.Client
	xsrfToken   string
	mfaId       string
	accessToken string
}

// New creates a new Client
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

// Login logs in to the DKB website using the provided credentials
func (c *Client) Login(username, password string) error {

	xsrfToken, err := c.getXsrfToken()

	if err != nil {
		return err
	}
	c.xsrfToken = xsrfToken

	err = c.postLoginCredentials(username, password)
	if err != nil {
		return err
	}

	r, err := c.newRequest(http.MethodGet, "https://banking.dkb.de/api/mfa/mfa/methods?filter%5BmethodType%5D=seal_one", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		return err
	}
	mfaMethodResponse := MFAMethodsResponse{}

	err = json.Unmarshal(buf.Bytes(), &mfaMethodResponse)
	if err != nil {
		return err
	}

	// TODO: Allow for selection of method to be used
	recentMethod := getMostRecentlyEnrolledMFAMethod(mfaMethodResponse.Data)
	fmt.Printf("%+v\n", recentMethod)

	ch := newMFAChallenge(recentMethod.ID, c.mfaId)
	chb, _ := json.Marshal(ch)
	r, err = c.newRequest(http.MethodPost, "https://banking.dkb.de/api/mfa/mfa/challenges", bytes.NewReader(chb))
	r.Header.Set("Content-Type", "application/vnd.api+json")
	resp, err = c.httpClient.Do(r)
	if err != nil {
		return err
	}

	b, _ := io.ReadAll(resp.Body)

	cr := MFAChallengeResponse{}
	err = json.Unmarshal(b, &cr)
	if err != nil {
		return err
	}

	err = c.pollVerificationStatus(cr.Data.ID)
	if err != nil {
		return err
	}

	err = c.postToken()
	if err != nil {
		return err
	}

	r, err = c.newRequest(http.MethodGet, "https://banking.dkb.de/api/accounts/accounts", nil)
	r.Header.Set("Content-Type", "application/vnd.api+json")
	resp, err = c.httpClient.Do(r)
	if err != nil {
		return err
	}
	b, _ = io.ReadAll(resp.Body)
	fmt.Printf("%+v", string(b))
	return nil
}

// TODO: Naming (or refactoring)
// currently this does more than just posting credentials: it also sets `c.mfa_id` and `c.accessToken`
func (c *Client) postLoginCredentials(username string, password string) error {
	data := url.Values{}

	data.Add("grant_type", "banking_user_sca")
	data.Add("sca_type", "web-login")
	data.Add("username", username)
	data.Add("password", password)

	r, err := c.newRequest(http.MethodPost, "https://banking.dkb.de/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}

	fmt.Println(resp.Status)

	buf := new(bytes.Buffer)

	_, err = buf.ReadFrom(resp.Body)
	if err != nil {
		return err
	}

	tokenData := TokenData{}

	err = json.Unmarshal(buf.Bytes(), &tokenData)
	if err != nil {
		return err
	}

	c.mfaId = tokenData.MfaID
	c.accessToken = tokenData.AccessToken

	return nil
}

func (c *Client) postToken() error {
	data := url.Values{}

	data.Add("grant_type", "banking_user_mfa")
	data.Add("mfa_id", c.mfaId)
	data.Add("access_token", c.accessToken)

	r, err := c.newRequest(http.MethodPost, "https://banking.dkb.de/api/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POSTing token failed: %v", err)
	}
	return nil
}

func (c *Client) newRequest(method string, url string, body io.Reader) (*http.Request, error) {
	r, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	r.Header.Set("x-xsrf-token", c.xsrfToken)
	return r, nil
}

func (c *Client) getXsrfToken() (string, error) {
	_, err := c.httpClient.Get("https://banking.dkb.de/login")
	if err != nil {
		return "", err
	}

	u, err := url.Parse("https://banking.dkb.de")
	if err != nil {
		return "", err
	}

	t := ""
	found := false

	for _, cookie := range c.httpClient.Jar.Cookies(u) {
		if cookie.Name == "__Host-xsrf" {
			t = cookie.Value
			found = true
			break
		}
	}
	if !found {
		return "", errors.New("XSRF cookie not found")
	}
	return t, nil
}
func (c *Client) pollVerificationStatus(cid string) error {
	pollID := time.Now().UTC().UnixMilli() * 1000
	pollURL := "https://banking.dkb.de/api/mfa/mfa/challenges/" + cid

	req, err := http.NewRequest(http.MethodGet, pollURL, nil)
	if err != nil {
		return err
	}
	for i := 0; i < 60; i++ {

		pollID++

		resp, _ := c.httpClient.Do(req)

		b, _ := io.ReadAll(resp.Body)

		c := MFAChallengeResponse{}
		err := json.Unmarshal(b, &c)
		if err != nil {
			return err
		}

		err = resp.Body.Close()
		if err != nil {
			return err
		}

		if c.Data.Attributes.VerificationStatus == "processed" {
			break
		}
		time.Sleep(3000 * time.Millisecond)
	}

	return nil
}

func (c *Client) GetAccounts() (Accounts, error) {
	req, err := c.newRequest(http.MethodGet, "https://banking.dkb.de/api/accounts/accounts", nil)
	if err != nil {
		return Accounts{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Accounts{}, err
	}

	b, _ := io.ReadAll(resp.Body)

	accounts := Accounts{}
	err = json.Unmarshal(b, &accounts)
	if err != nil {
		return Accounts{}, err

	}

	err = resp.Body.Close()
	if err != nil {
		return Accounts{}, err
	}

	return accounts, nil
}

func (c *Client) GetTransactions(accountID string) (Transactions, error) {
	tURL := "https://banking.dkb.de/api/accounts/accounts/" + accountID + "/transactions"
	req, err := c.newRequest(http.MethodGet, tURL, nil)
	if err != nil {
		return Transactions{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Transactions{}, err
	}

	b, _ := io.ReadAll(resp.Body)

	transactions := Transactions{}
	err = json.Unmarshal(b, &transactions)
	if err != nil {
		return Transactions{}, err

	}

	err = resp.Body.Close()
	if err != nil {
		return Transactions{}, err
	}

	return transactions, nil
}

// TODO: Move models to proper package

type MFAMethodsResponse struct {
	Data []MFAMethod `json:"data"`
}

type MFAMethod struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		MethodType                      string    `json:"methodType"`
		DeviceName                      string    `json:"deviceName"`
		Locked                          bool      `json:"locked"`
		RemainingValidationAttempts     int       `json:"remainingValidationAttempts"`
		RemainingChallenges             int       `json:"remainingChallenges"`
		MethodTypeOrder3Ds              int       `json:"methodTypeOrder3ds"`
		SealOneID                       string    `json:"sealOneId"`
		EnrolledAt                      time.Time `json:"enrolledAt"`
		Portfolio                       string    `json:"portfolio"`
		PreferredDevice                 bool      `json:"preferredDevice"`
		LockingPeriodAfterEnrollmentEnd time.Time `json:"lockingPeriodAfterEnrollmentEnd"`
	} `json:"attributes"`
}

func getMostRecentlyEnrolledMFAMethod(methods []MFAMethod) MFAMethod {
	mostRecentlyenrolledMethod := methods[0]
	for _, m := range methods {
		if m.Attributes.EnrolledAt.After(mostRecentlyenrolledMethod.Attributes.EnrolledAt) {
			mostRecentlyenrolledMethod = m
		}
	}
	return mostRecentlyenrolledMethod
}

type MFAChallenge struct {
	Data MFAChallengeData `json:"data"`
}

type MFAChallengeData struct {
	Attributes MFAChallengeDataAttributes `json:"attributes"`
	Type       string                     `json:"type"`
}

type MFAChallengeDataAttributes struct {
	MethodID   string `json:"methodId"`
	MethodType string `json:"methodType"`
	MfaID      string `json:"mfaId"`
}

func newMFAChallenge(methodId string, mfaID string) MFAChallenge {
	return MFAChallenge{Data: MFAChallengeData{Type: "mfa-challenge",
		Attributes: MFAChallengeDataAttributes{
			MethodID:   methodId,
			MethodType: "seal_one",
			MfaID:      mfaID,
		}}}
}

type MFAChallengeResponse struct {
	Data     MFAChallengeResponseData `json:"data"`
	Included []MFAMethod              `json:"included"`
}

type MFAChallengeResponseData struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	Attributes struct {
		MfaID              string `json:"mfaId"`
		MethodID           string `json:"methodId"`
		MethodType         string `json:"methodType"`
		VerificationStatus string `json:"verificationStatus"`
	} `json:"attributes"`
	Relationships struct {
		Method struct {
			Data struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"data"`
		} `json:"method"`
	} `json:"relationships"`
	Links struct {
		Self string `json:"self"`
	} `json:"links"`
}

type TokenData struct {
	AccessToken           string `json:"access_token"`
	RefreshTokenExpiresIn string `json:"refresh_token_expires_in"`
	RefreshToken          string `json:"refresh_token"`
	TokenFactorType       string `json:"token_factor_type"`
	AnonymousUserID       string `json:"anonymous_user_id"`
	Scope                 string `json:"scope"`
	IDToken               string `json:"id_token"`
	MfaID                 string `json:"mfa_id"`
	TokenType             string `json:"token_type"`
	ExpiresIn             int    `json:"expires_in"`
}

type Accounts struct {
	Data []Account `json:"data"`
}

type Account struct {
	Type       string            `json:"type"`
	Id         string            `json:"id"`
	Attributes AccountAttributes `json:"attributes"`
}

type AccountAttributes struct {
	HolderName                        string           `json:"holderName"`
	Iban                              string           `json:"iban"`
	Permissions                       []string         `json:"permissions"`
	CurrencyCode                      string           `json:"currencyCode"`
	Balance                           CurrencyValue    `json:"balance"`
	AvailableBalance                  CurrencyValue    `json:"availableBalance"`
	NearTimeBalance                   CurrencyValue    `json:"nearTimeBalance"`
	Product                           Product          `json:"product"`
	State                             string           `json:"state"`
	UpdatedAt                         string           `json:"updatedAt"`
	OpeningDate                       string           `json:"openingDate"`
	OverdraftLimit                    string           `json:"overdraftLimit"`
	OverdraftInterestRate             string           `json:"overdraftInterestRate,omitempty"`
	InterestRate                      string           `json:"interestRate"`
	UnauthorizedOverdraftInterestRate string           `json:"unauthorizedOverdraftInterestRate"`
	LastAccountStatementDate          string           `json:"lastAccountStatementDate"`
	ReferenceAccount                  ReferenceAccount `json:"referenceAccount,omitempty"`
}

type CurrencyValue struct {
	CurrencyCode string `json:"currencyCode"`
	Value        string `json:"value"`
}

type ReferenceAccount struct {
	Iban          string `json:"iban"`
	AccountNumber string `json:"accountNumber"`
	Blz           string `json:"blz"`
}
type Product struct {
	Id          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"displayName"`
}

type Transactions struct {
	Data []Transaction `json:"data"`
}

type Transaction struct {
	Type       string                `json:"type"`
	Id         string                `json:"id"`
	Attributes TransactionAttributes `json:"attributes"`
}

type TransactionAttributes struct {
	Status                  string        `json:"status"`
	BookingDate             string        `json:"bookingDate"`
	Description             string        `json:"description"`
	EndToEndId              string        `json:"endToEndId,omitempty"`
	TransactionType         string        `json:"transactionType"`
	PurposeCode             string        `json:"purposeCode,omitempty"`
	BusinessTransactionCode string        `json:"businessTransactionCode"`
	Amount                  CurrencyValue `json:"amount"`
	Creditor                struct {
		Name            string `json:"name,omitempty"`
		CreditorAccount struct {
			AccountNr string `json:"accountNr,omitempty"`
			Blz       string `json:"blz,omitempty"`
			Iban      string `json:"iban,omitempty"`
		} `json:"creditorAccount"`
		Agent struct {
			Bic string `json:"bic"`
		} `json:"agent,omitempty"`
		IntermediaryName string `json:"intermediaryName,omitempty"`
		Id               string `json:"id,omitempty"`
	} `json:"creditor"`
	Debtor struct {
		Name          string `json:"name,omitempty"`
		DebtorAccount struct {
			AccountNr string `json:"accountNr,omitempty"`
			Blz       string `json:"blz,omitempty"`
			Iban      string `json:"iban,omitempty"`
		} `json:"debtorAccount"`
		Agent struct {
			Bic string `json:"bic"`
		} `json:"agent,omitempty"`
		IntermediaryName string `json:"intermediaryName,omitempty"`
	} `json:"debtor"`
	IsRevocable bool   `json:"isRevocable"`
	ValueDate   string `json:"valueDate,omitempty"`
	MandateId   string `json:"mandateId,omitempty"`
}
