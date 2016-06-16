package main

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

const URL string = "https://pricing.us-east-1.amazonaws.com/offers/v1.0/aws/AmazonEC2/current/index.json"

const CACHE_DIR string = "/tmp/.aws_pricing"

const MAX_CACHE_AGE float64 = 86400

const AWS_LOCATION string = "US West (Oregon)"

type Offer struct {
	FormatVersion   string `json:"formatVersion"`   // The version of the file format
	Disclaimer      string `json:"disclaimer"`      // The disclaimers for the offer file
	OfferCode       string `json:"offerCode"`       // The code for the service
	Version         string `json:"version"`         // The version of the offer file
	PublicationDate string `json:"publicationDate"` // The publication date of the offer file

	Products map[string]Product                    `json:"products"` //Product Details
	Terms    map[string]map[string]map[string]Term `json:"terms"`    // Pricing Details (Terms)
}

type Product struct {
	SKU           string            `json:"sku"`           // The SKU of the product
	ProductFamily string            `json:"productFamily"` // The product family of the product
	Attributes    map[string]string `json:"attributes"`
}

type Term struct {
	OfferTermCode      string            `json:"offerTermCode"`      // The term code of the product
	SKU                string            `json:"sku"`                // The SKU of the product
	EffectiveDate      string            `json:"effectiveDate"`      // The effective date of the pricing details
	TermAttributesType string            `json:"termAttributesType"` // The attribute type of the terms
	TermAttributes     map[string]string `json:"termAttributes"`

	PriceDimensions map[string]PriceDimension `json:"priceDimensions"`
}

type PriceDimension struct {
	Description   string            `json:"description"`   // The description of the term
	Unit          string            `json:"unit"`          // The usage measurement unit for the price
	StartingRange string            `json:"startingRange"` // The start range for the term
	EndingRange   string            `json:"endingRange"`   // The end range for the term
	PricePerUnit  map[string]string `json:"pricePerUnit"`  // The rate code of the price
}

type Price struct {
	Currency string
	Value    string
}

func main() {

	// Command line arguments
	instance_type := flag.String("type", "m4.4xlarge", "EC2 Instance type")
	tenancy := flag.String("tenancy", "Shared", "EC2 Tenancy type")
	operating_system := flag.String("os", "Linux", "EC2 Operating system")
	term := flag.String("term", "OnDemand", "EC2 Term")

	flag.Parse()

	// Ensure the cache directory exists
	cache_dir_exists, err := exists(CACHE_DIR)
	if err != nil {
		log.Fatalf("Could not get offers: %s", err)
	}
	if !cache_dir_exists {
		if os.MkdirAll(CACHE_DIR, 0777) != nil {
			log.Fatalf("Could not get offers: %s", err)
		}
	}

	// Get the OnDemand price for an m4.4xlarge
	price, err := GetEC2Price(*instance_type, *tenancy, *operating_system, *term)
	if err != nil {
		log.Fatalf("Could not get EC2 Price: %s", err)
	}

	// Return the price to STDOUT
	log.Printf("Price (%f) found for %s %s %s %s", price, *term, *operating_system, *tenancy, *instance_type)
	fmt.Println(price)

}

func GetEC2Price(instance_type string, tenancy string, operating_system string, term string) (float64, error) {

	var price_f float64

	// Is this request in cache?
	cache_file := CACHE_DIR + "/" + GetMD5Hash(instance_type+tenancy+operating_system+term)
	stat, err := os.Stat(cache_file)
	if (err == nil) && (time.Since(stat.ModTime()).Seconds() < MAX_CACHE_AGE) {

		// Read body content from cache file
		log.Printf("Reading content from cache file: %s", cache_file)

		body, err := ioutil.ReadFile(cache_file)
		if err != nil {
			return 0, err
		}

		buf := bytes.NewReader(body)
		err = binary.Read(buf, binary.LittleEndian, &price_f)
		if err != nil {
			return 0, err
		}

	} else {

		// Get offers
		offer, err := GetOffers(URL)
		if err != nil {
			log.Fatalf("Could not get offers: %s", err)
		}

		// First we get the SKU
		sku, err := offer.GetSKU(instance_type, tenancy, operating_system)
		if err != nil {
			return 0, err
		}

		// Now get the price for this SKU
		price, err := offer.GetPrice(sku, term)
		if err != nil {
			return 0, err
		}

		// Convert the price string to a float64
		price_f, err = strconv.ParseFloat(price.Value, 64)
		if err != nil {
			return 0, err
		}

		// Save response to disk to speed up future requests
		buf := new(bytes.Buffer)
		err = binary.Write(buf, binary.LittleEndian, price_f)
		if err != nil {
			return 0, err
		}

		err = ioutil.WriteFile(cache_file, buf.Bytes(), 0777)
		if err != nil {
			return 0, err
		}
	}

	return price_f, nil

}

func (o *Offer) GetPrice(sku string, term string) (*Price, error) {

	for _, term := range o.Terms[term][sku] {
		for _, price_dimension := range term.PriceDimensions {
			for currency, value := range price_dimension.PricePerUnit {
				price := &Price{
					Currency: currency,
					Value:    value,
				}
				return price, nil
			}
		}
	}
	return nil, errors.New("Could not find price!")

}

func (o *Offer) GetSKU(instance_type string, tenancy string, os string) (string, error) {

	// Get the SKU for the Instance Type
	var sku string
	for _, product := range o.Products {
		if (product.Attributes["instanceType"] == instance_type) &&
			(product.Attributes["location"] == AWS_LOCATION) &&
			(product.Attributes["tenancy"] == tenancy) &&
			(product.Attributes["operatingSystem"] == os) {

			// Have we already found a matching SKU?
			if sku != "" {
				return "", errors.New("More than one SKU found!")
			}
			sku = product.SKU
		}
	}

	return sku, nil
}

func GetOffers(url string) (*Offer, error) {

	offer := &Offer{}
	var body []byte

	// Does a cached copy already exist?
	cache_file := CACHE_DIR + "/offers"

	stat, err := os.Stat(cache_file)
	if (err == nil) && (time.Since(stat.ModTime()).Seconds() < MAX_CACHE_AGE) {
		// Read body content from cache file
		log.Printf("Reading content from cache file: %s", cache_file)

		body, err = ioutil.ReadFile(cache_file)
		if err != nil {
			return nil, err
		}
	} else {
		// Fetch the offers from the AWS API
		log.Printf("Fetching content from AWS API: %s", url)

		res, err := http.Get(url)
		if err != nil {
			return nil, err
		}

		// Read repsonse body
		body, err = ioutil.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	// Save response to disk to speed up future requests
	err = ioutil.WriteFile(cache_file, body, 0777)
	if err != nil {
		return nil, err
	}

	// Parse the JSON response
	err = json.Unmarshal(body, offer)
	if err != nil {
		return nil, err
	}

	return offer, nil
}

// exists returns whether the given file or directory exists or not
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return true, err
}

func GetMD5Hash(text string) string {
	hasher := md5.New()
	hasher.Write([]byte(text))
	return hex.EncodeToString(hasher.Sum(nil))
}
