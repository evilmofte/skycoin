package gui

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"encoding/json"

	"github.com/stretchr/testify/mock"

	"github.com/stretchr/testify/require"

	"github.com/skycoin/skycoin/src/cipher"
	"github.com/skycoin/skycoin/src/coin"
	"github.com/skycoin/skycoin/src/util/fee"
	"github.com/skycoin/skycoin/src/visor"
	"github.com/skycoin/skycoin/src/wallet"
)

// Gateway RPC interface wrapper for daemon state
type FakeGateway struct {
	mock.Mock
	walletID string
	coins    uint64
	dst      cipher.Address
	t        *testing.T
}

func (gw *FakeGateway) Spend(wltID string, coins uint64, dest cipher.Address) (*coin.Transaction, error) {
	args := gw.Called(wltID, coins, dest)
	return args.Get(0).(*coin.Transaction), args.Error(1)
}

// GetWalletBalance returns balance pair of specific wallet
func (gw *FakeGateway) GetWalletBalance(wltID string) (wallet.BalancePair, error) {
	args := gw.Called(wltID)
	return args.Get(0).(wallet.BalancePair), args.Error(1)
}

// GetWallet returns the wallet by wltID
func (gw *FakeGateway) GetWallet(wltID string) (wallet.Wallet, error) {
	args := gw.Called(wltID)
	return args.Get(0).(wallet.Wallet), args.Error(1)
}

func (gw *FakeGateway) UpdateWalletLabel(wltID, label string) error {
	args := gw.Called(wltID, label)
	return args.Error(0)
}

func TestWalletSpendHandler(t *testing.T) {
	type httpBody struct {
		WalletID string
		Dst      string
		Coins    string
	}

	tt := []struct {
		name                          string
		method                        string
		url                           string
		body                          *httpBody
		status                        int
		err                           string
		walletID                      string
		coins                         uint64
		dst                           string
		gatewaySpendResult            *coin.Transaction
		gatewaySpendErr               error
		gatewayGetWalletBalanceResult wallet.BalancePair
		gatewayBalanceErr             error
		spendResult                   *SpendResult
	}{
		{
			"405",
			"GET",
			"/wallet/spend",
			nil,
			http.StatusMethodNotAllowed,
			"405 Method Not Allowed",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - no walletID",
			"POST",
			"/wallet/spend",
			&httpBody{},
			http.StatusBadRequest,
			"400 Bad Request - missing wallet id",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - no dst",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
			},
			http.StatusBadRequest,
			"400 Bad Request - missing destination address \"dst\"",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - bad dst addr",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      " 2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			},
			http.StatusBadRequest,
			"400 Bad Request - invalid destination address: Invalid base58 character",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - no coins",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			},
			http.StatusBadRequest,
			"400 Bad Request - invalid \"coins\" value",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - coins is string",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "foo",
			},
			http.StatusBadRequest,
			"400 Bad Request - invalid \"coins\" value",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - coins is negative value",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "-123",
			},
			http.StatusBadRequest,
			"400 Bad Request - invalid \"coins\" value",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - zero coins",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "0",
			},
			http.StatusBadRequest,
			"400 Bad Request - invalid \"coins\" value, must > 0",
			"0",
			0,
			"",
			nil,
			nil,
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - gw spend error txn no fee",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusBadRequest,
			"400 Bad Request - Transaction has zero coinhour fee",
			"123",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			fee.ErrTxnNoFee,
			wallet.BalancePair{},
			nil,
			&SpendResult{
				Error: fee.ErrTxnNoFee.Error(),
			},
		},
		{
			"400 - gw spend error spending unconfirmed",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusBadRequest,
			"400 Bad Request - please spend after your pending transaction is confirmed",
			"123",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			wallet.ErrSpendingUnconfirmed,
			wallet.BalancePair{},
			nil,
			&SpendResult{
				Error: wallet.ErrSpendingUnconfirmed.Error(),
			},
		},
		{
			"400 - gw spend error insufficient balance",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusBadRequest,
			"400 Bad Request - balance is not sufficient",
			"123",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			wallet.ErrInsufficientBalance,
			wallet.BalancePair{},
			nil,
			&SpendResult{
				Error: wallet.ErrInsufficientBalance.Error(),
			},
		},
		{
			"404 - gw spend error wallet not exist",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusNotFound,
			"404 Not Found",
			"123",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			wallet.ErrWalletNotExist,
			wallet.BalancePair{},
			nil,
			&SpendResult{
				Error: wallet.ErrWalletNotExist.Error(),
			},
		},
		{
			"500 - gw spend error",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "123",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusInternalServerError,
			"500 Internal Server Error - Spend error",
			"123",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			errors.New("Spend error"),
			wallet.BalancePair{},
			nil,
			&SpendResult{
				Error: "Spend error",
			},
		},
		{
			"200 - gw GetWalletBalance error",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "1234",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusOK,
			"",
			"1234",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			nil,
			wallet.BalancePair{},
			errors.New("GetWalletBalance error"),
			&SpendResult{
				Error: "Get wallet balance failed: GetWalletBalance error",
				Transaction: &visor.ReadableTransaction{
					Sigs:      []string{},
					In:        []string{},
					Out:       []visor.ReadableTransactionOutput{},
					Hash:      "78877fa898f0b4c45c9c33ae941e40617ad7c8657a307db62bc5691f92f4f60e",
					InnerHash: "0000000000000000000000000000000000000000000000000000000000000000",
				},
			},
		},
		{
			"200 - OK",
			"POST",
			"/wallet/spend",
			&httpBody{
				WalletID: "1234",
				Dst:      "2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
				Coins:    "12",
			},
			http.StatusOK,
			"",
			"1234",
			12,
			"2konv5no3DZvSMxf2GPVtAfZinfwqCGhfVQ",
			&coin.Transaction{},
			nil,
			wallet.BalancePair{},
			nil,
			&SpendResult{
				Balance: &wallet.BalancePair{},
				Transaction: &visor.ReadableTransaction{
					Length:    0,
					Type:      0,
					Hash:      "78877fa898f0b4c45c9c33ae941e40617ad7c8657a307db62bc5691f92f4f60e",
					InnerHash: "0000000000000000000000000000000000000000000000000000000000000000",
					Timestamp: 0,
					Sigs:      []string{},
					In:        []string{},
					Out:       []visor.ReadableTransactionOutput{},
				},
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &FakeGateway{
				walletID: tc.walletID,
				t:        t,
			}
			addr, _ := cipher.DecodeBase58Address(tc.dst)
			gateway.On("Spend", tc.walletID, tc.coins, addr).Return(tc.gatewaySpendResult, tc.gatewaySpendErr)
			gateway.On("GetWalletBalance", tc.walletID).Return(tc.gatewayGetWalletBalanceResult, tc.gatewayBalanceErr)

			v := url.Values{}
			if tc.body != nil {
				if tc.body.WalletID != "" {
					v.Add("id", tc.body.WalletID)
				}
				if tc.body.Dst != "" {
					v.Add("dst", tc.body.Dst)
				}
				if tc.body.Coins != "" {
					v.Add("coins", tc.body.Coins)
				}
			}

			req, err := http.NewRequest(tc.method, tc.url, bytes.NewBufferString(v.Encode()))
			require.NoError(t, err)
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(walletSpendHandler(gateway))

			handler.ServeHTTP(rr, req)

			status := rr.Code
			require.Equal(t, tc.status, status, "case: %s, handler returned wrong status code: got `%v` want `%v`", tc.name, status, tc.status)

			if status != http.StatusOK {
				require.Equal(t, tc.err, strings.TrimSpace(rr.Body.String()), "case: %s, handler returned wrong error message: got `%v`| %s, want `%v`",
					tc.name, strings.TrimSpace(rr.Body.String()), status, tc.err)
			} else {
				var msg SpendResult
				err := json.Unmarshal(rr.Body.Bytes(), &msg)
				require.NoError(t, err)
				require.Equal(t, *tc.spendResult, msg)
			}
		})
	}
}

func TestWalletGet(t *testing.T) {
	type httpBody struct {
		WalletID string
		Dst      string
		Coins    string
	}

	tt := []struct {
		name                   string
		method                 string
		url                    string
		body                   *httpBody
		status                 int
		err                    string
		walletId               string
		gatewayGetWalletResult wallet.Wallet
		gatewayGetWalletErr    error
	}{
		{
			"405",
			http.MethodPut,
			"/wallet",
			nil,
			http.StatusMethodNotAllowed,
			"405 Method Not Allowed",
			"0",
			wallet.Wallet{},
			nil,
		},
		{
			"400 - no walletId",
			http.MethodGet,
			"/wallet",
			nil,
			http.StatusBadRequest,
			"400 Bad Request - missing wallet id",
			"",
			wallet.Wallet{},
			nil,
		},
		{
			"400 - error from the `gateway.GetWallet(wltID)`",
			http.MethodGet,
			"/wallet",
			&httpBody{
				WalletID: "123",
			},
			http.StatusBadRequest,
			"400 Bad Request - wallet 123 doesn't exist",
			"123",
			wallet.Wallet{},
			errors.New("wallet 123 doesn't exist"),
		},
		{
			"200 - OK",
			http.MethodGet,
			"/wallet",
			&httpBody{
				WalletID: "1234",
			},
			http.StatusOK,
			"",
			"1234",
			wallet.Wallet{
				Meta:    map[string]string{},
				Entries: []wallet.Entry{},
			},
			nil,
		},
	}

	for _, tc := range tt {
		gateway := &FakeGateway{
			walletID: tc.walletId,
			t:        t,
		}
		gateway.On("GetWallet", tc.walletId).Return(tc.gatewayGetWalletResult, tc.gatewayGetWalletErr)
		v := url.Values{}
		var url = tc.url
		if tc.body != nil {
			if tc.body.WalletID != "" {
				v.Add("id", tc.body.WalletID)
			}
		}

		if len(v) > 0 {
			url += "?" + v.Encode()
		}

		req, err := http.NewRequest(tc.method, url, nil)
		require.NoError(t, err)

		rr := httptest.NewRecorder()
		handler := http.HandlerFunc(walletGet(gateway))

		handler.ServeHTTP(rr, req)

		status := rr.Code
		require.Equal(t, tc.status, status, "case: %s, handler returned wrong status code: got `%v` want `%v`",
			tc.name, status, tc.status)

		if status != http.StatusOK {
			require.Equal(t, tc.err, strings.TrimSpace(rr.Body.String()),
				"case: %s, handler returned wrong error message: got `%v`| %s, want `%v`",
				tc.name, strings.TrimSpace(rr.Body.String()), status, tc.err)
		} else {
			var msg wallet.Wallet
			err = json.Unmarshal(rr.Body.Bytes(), &msg)
			if err != nil {
				t.Errorf("fail unmarshal json response while 200 OK. body: %s, err: %s", rr.Body.String(), err)
			}
			require.Equal(t, tc.gatewayGetWalletResult, msg, tc.name)
		}
	}
}

func TestWalletBalanceHandler(t *testing.T) {
	type httpBody struct {
		WalletID string
		Dst      string
		Coins    string
	}
	tt := []struct {
		name                          string
		method                        string
		url                           string
		body                          *httpBody
		status                        int
		err                           string
		walletId                      string
		gatewayGetWalletBalanceResult wallet.BalancePair
		gatewayBalanceErr             error
		result                        *wallet.BalancePair
	}{
		{
			"405",
			"PUT",
			"/wallet/balance",
			nil,
			http.StatusMethodNotAllowed,
			"405 Method Not Allowed",
			"0",
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"400 - no walletId",
			"GET",
			"/wallet/balance",
			nil,
			http.StatusBadRequest,
			"400 Bad Request - missing wallet id",
			"0",
			wallet.BalancePair{},
			nil,
			nil,
		},
		{
			"404 - gw `wallet doesn't exist` error",
			"GET",
			"/wallet/balance",
			&httpBody{
				WalletID: "notFoundId",
			},
			http.StatusNotFound,
			"404 Not Found",
			"notFoundId",
			wallet.BalancePair{
				Confirmed: wallet.Balance{Coins: 0, Hours: 0},
				Predicted: wallet.Balance{Coins: 0, Hours: 0},
			},
			wallet.ErrWalletNotExist,
			&wallet.BalancePair{
				Confirmed: wallet.Balance{Coins: 0, Hours: 0},
				Predicted: wallet.Balance{Coins: 0, Hours: 0},
			},
		},
		{
			"500 - gw other error",
			"GET",
			"/wallet/balance",
			&httpBody{
				WalletID: "someId",
			},
			http.StatusInternalServerError,
			"500 Internal Server Error - gatewayBalanceError",
			"someId",
			wallet.BalancePair{
				Confirmed: wallet.Balance{Coins: 0, Hours: 0},
				Predicted: wallet.Balance{Coins: 0, Hours: 0},
			},
			errors.New("gatewayBalanceError"),
			&wallet.BalancePair{
				Confirmed: wallet.Balance{Coins: 0, Hours: 0},
				Predicted: wallet.Balance{Coins: 0, Hours: 0},
			},
		},
		{
			"200 - OK",
			"GET",
			"/wallet/balance",
			&httpBody{
				WalletID: "foo",
			},
			http.StatusOK,
			"",
			"foo",
			wallet.BalancePair{},
			nil,
			&wallet.BalancePair{},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &FakeGateway{
				walletID: tc.walletId,
				t:        t,
			}
			gateway.On("GetWalletBalance", tc.walletId).Return(tc.gatewayGetWalletBalanceResult, tc.gatewayBalanceErr)

			v := url.Values{}
			var url = tc.url
			if tc.body != nil {
				if tc.body.WalletID != "" {
					v.Add("id", tc.body.WalletID)
				}
			}
			if len(v) > 0 {
				url += "?" + v.Encode()
			}
			req, err := http.NewRequest(tc.method, url, bytes.NewBufferString(v.Encode()))
			require.NoError(t, err)
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(walletBalanceHandler(gateway))

			handler.ServeHTTP(rr, req)

			status := rr.Code
			require.Equal(t, tc.status, status, "case: %s, handler returned wrong status code: got `%v` want `%v`", tc.name, status, tc.status)
			if status != tc.status {
				t.Errorf("case: %s, handler returned wrong status code: got `%v` want `%v`",
					tc.name, status, tc.status)
			}
			if status != http.StatusOK {
				require.Equal(t, tc.err, strings.TrimSpace(rr.Body.String()), "case: %s, handler returned wrong error message: got `%v`| %s, want `%v`",
					tc.name, strings.TrimSpace(rr.Body.String()), status, tc.err)
			} else {
				var msg wallet.BalancePair
				err = json.Unmarshal(rr.Body.Bytes(), &msg)
				require.NoError(t, err)
				require.Equal(t, tc.result, &msg, tc.name)
			}
		})
	}
}

func TestUpdateWalletLabelHandler(t *testing.T) {
	type httpBody struct {
		WalletID string
		Label    string
	}

	tt := []struct {
		name                        string
		method                      string
		url                         string
		body                        *httpBody
		status                      int
		err                         string
		walletId                    string
		label                       string
		gatewayUpdateWalletLabelErr error
		responseBody                string
	}{
		{
			"405",
			http.MethodGet,
			"/wallet/update",
			&httpBody{},
			http.StatusMethodNotAllowed,
			"405 Method Not Allowed",
			"",
			"",
			nil,
			"",
		},
		{
			"400 - missing wallet id",
			http.MethodPost,
			"/wallet/update",
			&httpBody{},
			http.StatusBadRequest,
			"400 Bad Request - missing wallet id",
			"",
			"",
			nil,
			"",
		},
		{
			"400 - missing label",
			http.MethodPost,
			"/wallet/update",
			&httpBody{
				WalletID: "foo",
			},
			http.StatusBadRequest,
			"400 Bad Request - missing label",
			"foo",
			"",
			nil,
			"",
		},
		{
			"404 - gateway.UpdateWalletLabel ErrWalletNotExist",
			http.MethodPost,
			"/wallet/update",
			&httpBody{
				WalletID: "foo",
				Label:    "label",
			},
			http.StatusNotFound,
			"404 Not Found",
			"foo",
			"label",
			wallet.ErrWalletNotExist,
			"",
		},
		{
			"500 - gateway.UpdateWalletLabel error",
			http.MethodPost,
			"/wallet/update",
			&httpBody{
				WalletID: "foo",
				Label:    "label",
			},
			http.StatusInternalServerError,
			"500 Internal Server Error - gateway.UpdateWalletLabel error",
			"foo",
			"label",
			errors.New("gateway.UpdateWalletLabel error"),
			"",
		},
		{
			"200 OK",
			http.MethodPost,
			"/wallet/update",
			&httpBody{
				WalletID: "foo",
				Label:    "label",
			},
			http.StatusOK,
			"",
			"foo",
			"label",
			nil,
			"\"success\"",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &FakeGateway{
				t: t,
			}
			gateway.On("UpdateWalletLabel", tc.walletId, tc.label).Return(tc.gatewayUpdateWalletLabelErr)

			v := url.Values{}
			if tc.body != nil {
				if tc.body.WalletID != "" {
					v.Add("id", tc.body.WalletID)
				}
				if tc.body.Label != "" {
					v.Add("label", tc.body.Label)
				}
			}
			req, err := http.NewRequest(tc.method, tc.url, bytes.NewBufferString(v.Encode()))
			require.NoError(t, err)
			req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			handler := http.HandlerFunc(walletUpdateHandler(gateway))

			handler.ServeHTTP(rr, req)

			status := rr.Code
			require.Equal(t, tc.status, status, "case: %s, handler returned wrong status code: got `%v` want `%v`",
				tc.name, status, tc.status)

			if status != http.StatusOK {
				require.Equal(t, tc.err, strings.TrimSpace(rr.Body.String()), "case: %s, handler returned wrong error message: got `%v`| %s, want `%v`",
					tc.name, strings.TrimSpace(rr.Body.String()), status, tc.err)
			} else {
				require.Equal(t, tc.responseBody, rr.Body.String(), tc.name)
			}
		})
	}
}
