package rpc

import (
	sha "crypto"

	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"

	config "github.com/pokt-network/txbot/config"
	spec "github.com/pokt-network/txbot/spec"
)

const (
	Harmony  string = "0040" // Harmony mainnet shard 0.
	Ethereum string = "0021" // Ethereum mainnet.
	IPFS     string = "1111" // IPFS.

	Hasher = sha.SHA3_256
)

type RpcContext struct {
	Client             *spec.ClientWithResponses
	Context            context.Context
	SessionBlockHeight int64
}

func NewRpcContext(config config.Config) *RpcContext {
	client, clientErr := spec.NewClientWithResponses(fmt.Sprintf("%s/v1", config.PocketEndpoint))
	if clientErr != nil {
		panic("Could not initialize RPC client.")
	}
	return &RpcContext{
		Client:             client,
		Context:            context.TODO(), // Not imporant at the moment.
		SessionBlockHeight: int64(0),
	}
}

func QueryHeight(config config.Config, rpcCtx *RpcContext) {
	var body interface{}
	res, err := rpcCtx.Client.PostQueryHeightWithResponse(rpcCtx.Context, body)
	if err != nil {
		fmt.Println(err)
	}
	if res == nil {
		fmt.Println("ERROR: Please check your RPC endpoint.")
		return
	}
	if res.JSON200 != nil {
		fmt.Printf("Current Height: %d\n", *res.JSON200.Height)
	} else {
		fmt.Printf("Error querying height: %v\n", *res.HTTPResponse)
	}
}

func RelayHmy(config config.Config, rpcCtx *RpcContext) {
	data := `{"jsonrpc":"2.0", "method":"hmyv2_blockNumber", "params":[], "id":1}`
	relay(Harmony, data, config, rpcCtx)
}

func RelayEth(config config.Config, rpcCtx *RpcContext) {
	data := `{"jsonrpc":"2.0", "method":"eth_blockNumber", "params":[], "id":1}`
	relay(Ethereum, data, config, rpcCtx)
}

func relay(blockchain string, data string, config config.Config, rpcCtx *RpcContext) {
	appPrivKey := config.GetRandomAppPrivateKey()
	appPubKey := appPrivKey.PublicKey().RawString()

	// TODO: Randomize this after adding support for multiple pocket endpoints.
	clientPrivKey := config.ServicerPrivateKey.Key
	clientPubKey := clientPrivKey.PublicKey().RawString()

	// Get the blockchain service node.
	serviceNode := getServiceNode(clientPrivKey.PublicKey().Address().String(), rpcCtx)
	if serviceNode == nil {
		fmt.Println("Could not find service node for key:", clientPubKey)
		return
	}

	// Prepare metadata.
	meta := spec.RelayMetadata{
		BlockHeight: &rpcCtx.SessionBlockHeight,
	}

	// Prepare payload.
	headers := spec.RelayHeader{
		AdditionalProperties: make(map[string]string),
	}
	method := "POST"
	path := "" // TODO: What should this be?
	payload := spec.RelayPayload{
		Data:    &data,
		Method:  &method,
		Path:    &path,
		Headers: &headers,
	}

	// Prepare request. NOTE: request serialization need to be customized.
	request := struct {
		Payload spec.RelayPayload  `json:"payload"`
		Meta    spec.RelayMetadata `json:"meta"`
	}{payload, meta}

	// Compute request hash.
	requestBytes, err := json.Marshal(request)
	if err != nil {
		fmt.Println(err)
		return
	}
	requestHash := hash(requestBytes)
	requestHashString := hex.EncodeToString(requestHash)

	// Prepare AAT.
	aatVersion := "0.0.1"
	aat := spec.AAT{
		AppPubKey:    &appPubKey,
		ClientPubKey: &clientPubKey,
		Signature:    new(string),
		Version:      &aatVersion,
	}

	// Sign AAT.
	aatBytes, err := json.Marshal(aat)
	if err != nil {
		fmt.Println(err)
		return
	}
	aatHash := hash(aatBytes)
	appSig, err := appPrivKey.Sign(aatHash)
	if err != nil {
		fmt.Println(err)
		return
	}
	appSigString := hex.EncodeToString(appSig)
	aat.Signature = &appSigString

	// Prepare proof.
	entropy := int64(rand.Uint32())
	servicerPubKey := *serviceNode.PublicKey
	proof := spec.RelayProof{
		Aat:                &aat,
		Blockchain:         &blockchain,
		Entropy:            &entropy,
		RequestHash:        &requestHashString,
		ServicerPubKey:     &servicerPubKey,
		SessionBlockHeight: &rpcCtx.SessionBlockHeight,
		Signature:          new(string),
	}

	// NOTE: proof serialization need to be customized.
	proofForSig := struct {
		Entropy            int64  `json:"entropy"`
		SessionBlockHeight int64  `json:"session_block_height"`
		ServicerPubKey     string `json:"servicer_pub_key"`
		Blockchain         string `json:"blockchain"`
		Signature          string `json:"signature"`
		Token              string `json:"token"`
		RequestHash        string `json:"request_hash"`
	}{entropy, rpcCtx.SessionBlockHeight, servicerPubKey, blockchain, "", hex.EncodeToString(aatHash), requestHashString}

	// Sign proof.
	proofBytes, err := json.Marshal(proofForSig)
	if err != nil {
		fmt.Println(err)
		return
	}
	proofHash := hash(proofBytes)
	clientSig, err := clientPrivKey.Sign(proofHash)
	if err != nil {
		fmt.Println(err)
		return
	}
	clientSigString := hex.EncodeToString(clientSig)
	proof.Signature = &clientSigString

	// Prepare relay request.
	body := spec.PostClientRelayJSONRequestBody{
		Meta:    &meta,
		Payload: &payload,
		Proof:   &proof,
	}

	// Do relay.
	res, err := rpcCtx.Client.PostClientRelayWithResponse(rpcCtx.Context, body)
	if err != nil {
		fmt.Println("PostClientRelayWithResponse error: ", err.Error())
		return
	}

	if res == nil {
		fmt.Println("ERROR: Please check your RPC endpoint.")
		return
	}

	switch res.StatusCode() {
	case 200:
		{
			fmt.Println(string(res.Body))
		}
	case 400:
		{
			if res.JSON400.Error != nil {
				fmt.Println("Error sending relay: ", *res.JSON400.Error.Message)
			}
			// Other errors could potentially happen but we're only accounting
			// for incorrect session block height for now.
			if res.JSON400.Dispatch != nil {
				rpcCtx.SessionBlockHeight = *res.JSON400.Dispatch.Session.Header.SessionHeight
				fmt.Println("The session block height has been updated. Please try re-sending the relay.")
			}
			return
		}
	default:
		fmt.Println("Unexpected status code: ", res.StatusCode())
	}
}

func getServiceNode(address string, rpcCtx *RpcContext) (val *spec.Node) {
	body := spec.PostQueryNodeJSONRequestBody{
		Address: &address,
	}
	res, err := rpcCtx.Client.PostQueryNodeWithResponse(rpcCtx.Context, body)
	if err != nil {
		fmt.Println(err)
		val = nil
		return
	}
	val = res.JSON200
	return
}

func hash(b []byte) []byte {
	hasher := Hasher.New()
	hasher.Write(b) //nolint:golint,errcheck
	return hasher.Sum(nil)
}
