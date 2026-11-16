package enrichment

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizePriceAmount(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		expected string
	}{
		{"a whole amount", "24500000", "24500000"},
		{"an amount with a fraction", "129.99", "129.99"},
		{"surrounding whitespace is trimmed", "  24500000  ", "24500000"},
		{"zero is a valid amount", "0", "0"},

		{"grouping separators are not parsed", "24.500.000", ""},
		{"a currency symbol is not stripped", "Rp24.500.000", ""},
		{"a negative value is refused", "-5", ""},
		{"two dots are refused", "1.2.3", ""},
		{"a leading dot is refused", ".99", ""},
		{"a trailing dot is refused", "1.", ""},
		{"letters are refused", "abc", ""},
		{"an empty value is refused", "", ""},
		{"whitespace only is refused", "   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, normalizePriceAmount(tc.value))
		})
	}
}

func TestNormalizeCurrency(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		expected string
	}{
		{"rupiah label", "Rp", "IDR"},
		{"rupiah label lower case", "rp", "IDR"},
		{"rupiah code", "IDR", "IDR"},
		{"rupiah code lower case", "idr", "IDR"},
		{"dollar code", "USD", "USD"},
		{"dollar code lower case", "usd", "USD"},
		{"us dollar symbol", "US$", "USD"},
		{"us dollar words", "US Dollar", "USD"},
		{"us dollars words", "US Dollars", "USD"},
		{"surrounding whitespace is trimmed", "  Rp  ", "IDR"},

		// A bare dollar sign is ambiguous across currencies and must never be
		// guessed at.
		{"a bare dollar sign is unmapped", "$", ""},
		{"an unknown code is unmapped", "EUR", ""},
		{"an unknown label is unmapped", "Rupee", ""},
		{"an empty value is unmapped", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, normalizeCurrency(tc.value))
		})
	}
}

// The verified Tokopedia shape: the product price is published as Open Graph
// product metadata and both values are present.
func TestSelectPrice_FromProductMetadata(t *testing.T) {
	metadata := extractFrom(
		t,
		`<html><head>
			<title>Microsoft Surface Pro 12</title>
			<meta property="product:price:amount" content="24500000" />
			<meta property="product:price:currency" content="Rp" />
		</head></html>`,
		"https://www.tokopedia.com/jullindo/microsoft-surface-pro-12",
	)

	require.NotNil(t, metadata.Price)
	require.Equal(t, "24500000", metadata.Price.Amount)
	require.Equal(t, "IDR", metadata.Price.Currency)
	require.False(t, metadata.IsEmpty())
}

// A valid amount with an unknown currency is still a price; only the currency is
// left empty.
func TestSelectPrice_ValidAmountUnknownCurrency(t *testing.T) {
	metadata := extractFrom(
		t,
		`<html><head>
			<title>Something</title>
			<meta property="product:price:amount" content="129.99" />
			<meta property="product:price:currency" content="$" />
		</head></html>`,
		"https://example.com/product",
	)

	require.NotNil(t, metadata.Price)
	require.Equal(t, "129.99", metadata.Price.Amount)
	require.Empty(t, metadata.Price.Currency)
}

func TestSelectPrice_InvalidAmountIsIgnored(t *testing.T) {
	cases := []string{
		"24.500.000",
		"Rp24.500.000",
		"abc",
		"-5",
		"",
	}

	for _, amount := range cases {
		t.Run(amount, func(t *testing.T) {
			metadata := extractFrom(
				t,
				`<html><head>
					<title>Something</title>
					<meta property="product:price:amount" content="`+amount+`" />
					<meta property="product:price:currency" content="Rp" />
				</head></html>`,
				"https://example.com/product",
			)

			require.Nil(t, metadata.Price)
		})
	}
}

// Without a product price tag, unrelated numbers in the page or in serialized
// application state are never selected as a price.
func TestSelectPrice_DoesNotScanArbitraryNumbers(t *testing.T) {
	document := `<html><head>
		<title>Something</title>
		<script>window.__STATE__ = {"variants":[{"price":1500000},{"price":2500000}]}</script>
	</head><body>
		<p>Only 24500000 units left at Rp 24.500.000</p>
	</body></html>`

	metadata := extractFrom(t, document, "https://example.com/product")

	require.Nil(t, metadata.Price)
}

// A price without any title still makes the extraction non-empty. It is the
// persistence layer, not the extractor, that declines to store price without a
// title.
func TestSelectPrice_PriceOnlyIsNotEmpty(t *testing.T) {
	metadata := extractFrom(
		t,
		`<html><head>
			<meta property="product:price:amount" content="24500000" />
			<meta property="product:price:currency" content="Rp" />
		</head></html>`,
		"https://example.com/product",
	)

	require.Nil(t, metadata.Title)
	require.NotNil(t, metadata.Price)
	require.False(t, metadata.IsEmpty())
}

func TestMetadataIsEmpty_PriceOnly(t *testing.T) {
	require.False(t, Metadata{Price: &Price{Amount: "1"}}.IsEmpty())
	require.True(t, Metadata{}.IsEmpty())
}

func TestNormalizeStructuredPriceAmount(t *testing.T) {
	cases := []struct {
		name     string
		value    string
		currency string
		expected string
	}{
		{"IDR groups with spaces", "85 800", "IDR", "85800"},
		{"IDR groups with non-breaking spaces", "85\u00a0800", "IDR", "85800"},
		{"IDR groups with dots", "24.500.000", "IDR", "24500000"},
		{"IDR plain", "24500000", "IDR", "24500000"},
		{"USD groups with commas", "1,299.99", "USD", "1299.99"},
		{"USD plain", "49.99", "USD", "49.99"},

		{"a currency symbol is not stripped", "Rp 85 800", "IDR", ""},
		{"a negative value is refused", "-5", "USD", ""},
		{"an unsupported currency is not normalized", "59.99", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, normalizeStructuredPriceAmount(tc.value, tc.currency))
		})
	}
}

// A storefront's JSON-LD offer is a price the page published for the product,
// not an arbitrary number found in the page.
func TestSelectPrice_FromJSONLDOffer(t *testing.T) {
	cases := []struct {
		name     string
		document string
		amount   string
		currency string
	}{
		{
			name: "an Offer object",
			document: `<script type="application/ld+json">
				{"@context":"https://schema.org","@type":"Product",
				 "offers":{"@type":"Offer","price":39.99,"priceCurrency":"USD"}}
			</script>`,
			amount:   "39.99",
			currency: "USD",
		},
		{
			name: "an AggregateOffer uses lowPrice",
			document: `<script type="application/ld+json">
				{"@context":"https://schema.org","@type":"Product",
				 "offers":{"@type":"AggregateOffer","lowPrice":49.99,"highPrice":69.99,
				 "priceCurrency":"USD","offers":[
					{"@type":"Offer","price":49.99,"priceCurrency":"USD"},
					{"@type":"Offer","price":69.99,"priceCurrency":"USD"}]}}
			</script>`,
			amount:   "49.99",
			currency: "USD",
		},
		{
			name: "an array of offers uses the first usable price",
			document: `<script type="application/ld+json">
				{"@context":"https://schema.org","@type":"Product",
				 "offers":[{"@type":"Offer","price":"59.99","priceCurrency":"USD"}]}
			</script>`,
			amount:   "59.99",
			currency: "USD",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := extractFrom(t, `<html><head><title>Product</title>`+tc.document+`</head></html>`, "https://example.com/product")

			require.NotNil(t, metadata.Price)
			require.Equal(t, tc.amount, metadata.Price.Amount)
			require.Equal(t, tc.currency, metadata.Price.Currency)
		})
	}
}

// Steam publishes the price as meta microdata, with the amount grouped by
// spaces in IDR and by a comma decimal in EUR.
func TestSelectPrice_FromMicrodata(t *testing.T) {
	cases := []struct {
		name     string
		currency string
		amount   string
		expected *Price
	}{
		{
			name:     "IDR with spaces",
			currency: "IDR",
			amount:   "85 800",
			expected: &Price{Amount: "85800", Currency: "IDR"},
		},
		{
			name:     "USD with a dot decimal",
			currency: "USD",
			amount:   "7.99",
			expected: &Price{Amount: "7.99", Currency: "USD"},
		},
		{
			name:     "an unsupported currency is not stored",
			currency: "EUR",
			amount:   "7,99",
			expected: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata := extractFrom(
				t,
				`<html><head><title>Product</title>
					<meta itemprop="priceCurrency" content="`+tc.currency+`">
					<meta itemprop="price" content="`+tc.amount+`">
				</head></html>`,
				"https://store.steampowered.com/app/2058190/Lost_Judgment/",
			)

			if tc.expected == nil {
				require.Nil(t, metadata.Price)

				return
			}

			require.NotNil(t, metadata.Price)
			require.Equal(t, tc.expected.Amount, metadata.Price.Amount)
			require.Equal(t, tc.expected.Currency, metadata.Price.Currency)
		})
	}
}

// Open Graph tags are published for this exact purpose and still win over a
// storefront's structured data.
func TestSelectPrice_OpenGraphBeatsStructuredData(t *testing.T) {
	metadata := extractFrom(
		t,
		`<html><head>
			<title>Product</title>
			<meta property="product:price:amount" content="10.00">
			<meta property="product:price:currency" content="USD">
			<script type="application/ld+json">
				{"@type":"Product","offers":{"@type":"Offer","price":99.99,"priceCurrency":"USD"}}
			</script>
		</head></html>`,
		"https://example.com/product",
	)

	require.NotNil(t, metadata.Price)
	require.Equal(t, "10.00", metadata.Price.Amount)
	require.Equal(t, "USD", metadata.Price.Currency)
}
