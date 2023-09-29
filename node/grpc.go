package node

import (
	"context"
	"encoding/hex"
	"log"
	"net"
	"net/http"

	ethcommon "github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/solace-labs/skeyn/common"
	proto "github.com/solace-labs/skeyn/proto"
	protob "google.golang.org/protobuf/proto"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TODO: Handle errors
func (n *Node) SetupGRPC(ctx context.Context) {
	lis, err := net.Listen("tcp", ":5123")
	if err != nil {
		panic(err)
	}
	grpcServer := grpc.NewServer()
	proto.RegisterTransactionServiceServer(grpcServer, n)

	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	if err := proto.RegisterTransactionServiceHandlerFromEndpoint(ctx, mux, "localhost:5123", opts); err != nil {
		panic(err)
	}

	go func() {
		log.Println("Running GRPC/HTTP on port", 5050)
		log.Fatal(http.ListenAndServe(":5050", mux))
	}()

	log.Println("Running GRPC Server on port", 5123)
	go func() {
		log.Fatal(grpcServer.Serve(lis))
	}()
}

// 1. Check if the wallet is under management
// 2. Check if the signer of the message is authorzed
// 3. Check if the type of message is valid (Start DKG vs Sign)
// 4. Signal the squad to do what's required
// Called by external agents
func (n *Node) HandleTransaction(ctx context.Context, req *proto.Transaction) (*proto.TransactionResponse, error) {
	walletAddressEth := ethcommon.HexToAddress(req.Payload.WalletAddress)
	signature := ethcommon.Hex2Bytes(req.Payload.Signature)
	data := ethcommon.Hex2Bytes(req.Payload.Data)

	isInvalidRequest := req.Type == "" ||
		req.Payload == nil ||
		walletAddressEth.Bytes() == nil ||
		signature == nil ||
		data == nil

	if isInvalidRequest {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request"}, nil
	}

	signDataHash := ethcrypto.Keccak256Hash(data)
	pubKey, err := ethcrypto.SigToPub(signDataHash.Bytes(), signature)
	if err != nil {
		return &proto.TransactionResponse{Success: false, Msg: err.Error()}, nil
	}

	address := ethcrypto.PubkeyToAddress(*pubKey)
	log.Println("Address - ", address.Hex())

	walletAddr := common.NewEthWalletAddress(walletAddressEth)
	n.SetupSquad(ctx, walletAddr)

	// Check if address and wallet address are a part of the squad using the network state
	// If yes, check if it exists
	//	Create if doesn't exist
	// If no reject

	// TODO: Broadcast random shit to peers
	if req.Type == "1" {
		n.squad[walletAddr].InitKeygen(ctx)
	} else {
		// Verify Incoming Message
		// n.squad[walletAddr].InitSigning(ctx, data)
	}

	key := hex.EncodeToString(data)

	// Your logic here
	return &proto.TransactionResponse{Success: true, Msg: key}, nil
}

func (n *Node) HandleSigRetrieval(ctx context.Context, req *proto.SignatureRetrieval) (*proto.TransactionResponse, error) {
	walletAddressEth := ethcommon.HexToAddress(req.WalletAddress)
	if walletAddressEth.Bytes() == nil {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request [1]"}, nil
	}

	walletAddr := common.NewEthWalletAddress(walletAddressEth)

	if _, exists := n.squad[walletAddr]; !exists {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request [2]"}, nil
	}

	sig, err := n.squad[walletAddr].GetSig([]byte(req.Key))
	if err != nil {

		return &proto.TransactionResponse{Success: false, Msg: "Error fetching squad sig"}, err
	}
	return &proto.TransactionResponse{Success: true, Msg: hex.EncodeToString(sig)}, nil
}

// Create a rule for a smart-contract wallet
func (n *Node) HandleCreateRule(ctx context.Context, req *proto.CreateRuleData) (*proto.TransactionResponse, error) {
	// TOOD: Check if the message is coming from the owner of the SCW
	walletAddressEth := ethcommon.HexToAddress(req.WalletAddress)
	if walletAddressEth.Bytes() == nil {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request [1]"}, nil
	}

	walletAddr := common.NewEthWalletAddress(walletAddressEth)

	if _, exists := n.squad[walletAddr]; !exists {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request [2]"}, nil
	}

	sqd := n.squad[walletAddr]
	err := sqd.CreateRule(req)
	if err != nil {
		return &proto.TransactionResponse{Success: false, Msg: err.Error()}, nil
	}

	outMsg, err := protob.Marshal(req)
	if err != nil {
		return &proto.TransactionResponse{Success: false, Msg: "Error marshalling rul"}, nil
	}
	// TODO: Broadcasting here to avoid recurring broadcasts. This is a short term solution
	sqd.Broadcast(common.CREATE_RULE, outMsg)

	return &proto.TransactionResponse{Success: true, Msg: "Rule stored"}, nil
}

func (n *Node) HandleSignatureRequest(ctx context.Context, req *proto.SolaceTx) (*proto.TransactionResponse, error) {
	log.Println("Handling Sign Request")
	log.Printf("%#v \n", req)
	walletAddressEth := ethcommon.HexToAddress(req.WalletAddr)

	if walletAddressEth.Bytes() == nil {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request [1]"}, nil
	}

	walletAddr := common.NewEthWalletAddress(walletAddressEth)
	if _, exists := n.squad[walletAddr]; !exists {
		return &proto.TransactionResponse{Success: false, Msg: "Invalid Request [2]"}, nil
	}

	err := n.squad[walletAddr].ValidateSolaceTx(req)
	if err != nil {
		return &proto.TransactionResponse{Success: false, Msg: err.Error()}, nil
	}
	n.squad[walletAddr].InitSigning(req)

	return &proto.TransactionResponse{Success: true, Msg: "Sign Inited"}, nil
}

func (n Node) HandleMetricsQuery(ctx context.Context, req *proto.Empty) (*proto.MetricsResponse, error) {
	resp := &proto.MetricsResponse{Peers: make([]string, 0), Squads: make([]*proto.Squad, 0)}

	for _, peer := range n.h().Network().Peers() {
		resp.Peers = append(resp.Peers, peer.ShortString())
	}
	resp.Peers = append(resp.Peers, n.h().ID().ShortString())

	for _, val := range n.squad {
		s := &proto.Squad{
			WalletAddr: val.ID,
			Signatures: val.GetTransactions(),
		}
		resp.Squads = append(resp.Squads, s)
	}
	return resp, nil
}
