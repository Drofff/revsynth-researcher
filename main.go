package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/Drofff/revsynth/aco"
	"github.com/Drofff/revsynth/circuit"
	"github.com/Drofff/revsynth/logging"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconf "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	awstypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/google/uuid"
)

type AlgConfig struct {
	NumOfAnts       int     `json:"numOfAnts"`
	NumOfIterations int     `json:"numOfIterations"`
	Alpha           float64 `json:"alpha"`
	Beta            float64 `json:"beta"`
	EvaporationRate float64 `json:"evaporationRate"`
	LocalLoops      int     `json:"localLoops"`
	SearchDepth     int     `json:"searchDepth"`
}

type Input struct {
	TargetQuantumCost int         `json:"targetQuantumCost"`
	AcoConfigs        []AlgConfig `json:"acoConfigs"`
	InputTT           [][]int     `json:"inputTT"`
	TargetVector      []int       `json:"targetVector"`
}

type Solution struct {
	QuantumCost  int
	TargetVector []int
	Gates        []circuit.Gate
}

type Repository interface {
	SaveSolution(ctx context.Context, s Solution) error
}

type ddbRepository struct {
	ddbClient *dynamodb.Client
}

const (
	depositStrengthDefault = 100

	dynamoDBTableName    = "revsynth-research-results"
	dynamoDBPartitionKey = "id"
	dynamoDBSortKey      = "truthVector"
	dynamoDBQCKey        = "quantumCost"
	dynamoDBGatesKey     = "gates"
)

func vectorToStr(v []int) string {
	vss := make([]string, 0)
	for _, el := range v {
		vss = append(vss, strconv.Itoa(el))
	}

	return "[" + strings.Join(vss, ", ") + "]"
}

func gatesToStr(gates []circuit.Gate) string {
	gatesSS := make([]string, 0)

	for i := len(gates) - 1; i >= 0; i-- {
		gateS := gates[i].TypeName() + "(" + vectorToStr(gates[i].TargetBits()) + ", " + vectorToStr(gates[i].ControlBits()) + ")"
		gatesSS = append(gatesSS, gateS)
	}

	return strings.Join(gatesSS, ", ")
}

func (r *ddbRepository) SaveSolution(ctx context.Context, s Solution) error {
	id := uuid.NewString()
	vectorS := vectorToStr(s.TargetVector)
	gatesS := gatesToStr(s.Gates)

	_, err := r.ddbClient.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(dynamoDBTableName),
		Item: map[string]awstypes.AttributeValue{
			dynamoDBPartitionKey: &awstypes.AttributeValueMemberS{
				Value: id,
			},
			dynamoDBSortKey: &awstypes.AttributeValueMemberS{
				Value: vectorS,
			},
			dynamoDBQCKey: &awstypes.AttributeValueMemberN{
				Value: strconv.Itoa(s.QuantumCost),
			},
			dynamoDBGatesKey: &awstypes.AttributeValueMemberS{
				Value: gatesS,
			},
		},
	})
	return err
}

func readInput(filename string) Input {
	content, err := os.ReadFile(filename)
	if err != nil {
		log.Fatalln(err)
	}

	in := &Input{}
	err = json.Unmarshal(content, in)
	if err != nil {
		log.Fatalln(err)
	}

	return *in
}

func createDDBRepository(ctx context.Context) Repository {
	conf, err := awsconf.LoadDefaultConfig(ctx, func(opts *awsconf.LoadOptions) error {
		opts.Region = "us-east-1"
		return nil
	})
	if err != nil {
		log.Fatalln("Configure AWS:", err)
	}

	ddbClient := dynamodb.NewFromConfig(conf)
	return &ddbRepository{ddbClient: ddbClient}
}

func calcQuantumCost(gates []circuit.Gate) int {
	qc := 0
	for _, gate := range gates {
		switch gate.TypeName() {
		case "fredkin":
			qc += 5
		case "cnot":
			qc += 1
		default:
			log.Fatalln("unknown gate type:", gate.TypeName())
		}
	}
	return qc
}

func main() {
	ctx := context.Background()
	in := readInput("input.json")
	repo := createDDBRepository(ctx)

	for {
		log.Println("Running next iteration")
		for _, acoConfig := range in.AcoConfigs {
			log.Println("Running next config")
			conf := aco.Config{
				NumOfAnts:       acoConfig.NumOfAnts,
				NumOfIterations: acoConfig.NumOfIterations,
				Alpha:           acoConfig.Alpha,
				Beta:            acoConfig.Beta,
				EvaporationRate: acoConfig.EvaporationRate,
				DepositStrength: depositStrengthDefault,
				LocalLoops:      acoConfig.LocalLoops,
				SearchDepth:     acoConfig.SearchDepth,
			}

			synth := aco.NewSynthesizer(conf,
				[]circuit.GateFactory{circuit.NewCnotGateFactory(), circuit.NewFredkinGateFactory()},
				logging.NewLogger(logging.LevelInfo))

			res := synth.Synthesise(circuit.TruthVector{
				Inputs: in.InputTT,
				Vector: in.TargetVector,
			})

			if res.Complexity > 0 {
				log.Println("Skipping as complexity is", res.Complexity)
				continue
			}

			qc := calcQuantumCost(res.Gates)
			if qc > in.TargetQuantumCost {
				log.Println("Skipping as quantum cost is", qc)
				continue
			}

			err := repo.SaveSolution(ctx, Solution{
				QuantumCost:  qc,
				TargetVector: in.TargetVector,
				Gates:        res.Gates,
			})
			if err != nil {
				log.Fatalln("Failed to save:", err)
			}
		}
	}
}
