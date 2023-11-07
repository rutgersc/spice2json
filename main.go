package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	ns "github.com/authzed/spicedb/pkg/namespace"
	corev1 "github.com/authzed/spicedb/pkg/proto/core/v1"
	iv1 "github.com/authzed/spicedb/pkg/proto/impl/v1"
	"github.com/authzed/spicedb/pkg/schemadsl/compiler"
	"github.com/authzed/spicedb/pkg/schemadsl/input"
)

func main() {
	namespace := flag.String("n", "", "default namespace")
	flag.Parse()

	inputFileName := flag.Arg(0)
	if inputFileName == "" {
		displayUsageInfo()
		os.Exit(1)
	}

	b, err := os.ReadFile(inputFileName) // just pass the file name
	if err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
	schemaSource := string(b) // convert content to a 'string'

	in := compiler.InputSchema{
		Source:       input.Source(inputFileName),
		SchemaString: schemaSource,
	}

	def, _ := compiler.Compile(in, namespace)
	var buf strings.Builder
	err = WriteSchemaTo(def, &buf)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	output, _ := PrettyString(buf.String())

	outputFileName := flag.Arg(1)
	if outputFileName != "" {
		data := []byte(output)
		err = os.WriteFile(outputFileName, data, 0644)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	} else {
		fmt.Print(output)
	}
}

func displayUsageInfo() {
	fmt.Println("")
	fmt.Println("Please provide a valid input schema and a path to the output json")
	fmt.Println("")
	fmt.Println("Example: spice2json [-n namespace] input_schema.zed [output.json]")
	fmt.Println("")
}

// PrettyString https://gosamples.dev/pretty-print-json/
func PrettyString(str string) (string, error) {
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(str), "", "  "); err != nil {
		return "", err
	}
	return prettyJSON.String(), nil
}

// WriteSchemaTo Portions of this code were pulled from https://github.com/oviva-ag/spicedb
func WriteSchemaTo(schema *compiler.CompiledSchema, w io.Writer) error {
	var definitions []*Definition
	for _, def := range schema.ObjectDefinitions {
		o, err := mapDefinition(def)
		if err != nil {
			return fmt.Errorf("failed to export %q: %w", def.Name, err)
		}
		definitions = append(definitions, o)
	}

	var caveats []*Caveat
	for _, caveat := range schema.CaveatDefinitions {
		o := mapCaveat(caveat)
		caveats = append(caveats, o)
	}

	data, err := json.Marshal(&Schema{
		Definitions: definitions,
		Caveats:     caveats,
	})
	if err != nil {
		return fmt.Errorf("unable to serialize schema for export: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("unable to write schema for export: %w", err)
	}
	return nil
}

func mapDefinition(def *corev1.NamespaceDefinition) (*Definition, error) {
	var relations []*Relation
	var permissions []*Permission
	for _, r := range def.Relation {
		kind := ns.GetRelationKind(r)
		if kind == iv1.RelationMetadata_PERMISSION {
			permissions = append(permissions, mapPermission(r))
		} else if kind == iv1.RelationMetadata_RELATION {
			relations = append(relations, mapRelation(r))
		} else {
			return nil, fmt.Errorf("unexpected relation %q, neither permission nor relation", r.Name)
		}
	}

	splits := strings.SplitN(def.Name, "/", 2)
	var name string
	var namespace string
	if len(splits) == 2 {
		namespace = splits[0]
		name = splits[1]
	} else {
		name = splits[0]
		namespace = ""
	}

	return &Definition{
		Name:        name,
		Namespace:   namespace,
		Relations:   relations,
		Permissions: permissions,
		Comment:     getMetadataComments(def.GetMetadata()),
	}, nil
}

func mapRelation(relation *corev1.Relation) *Relation {
	var types []*RelationType
	for _, t := range relation.TypeInformation.AllowedDirectRelations {
		types = append(types, mapRelationType(t))
	}

	return &Relation{
		Name:    relation.Name,
		Comment: getMetadataComments(relation.GetMetadata()),
		Types:   types,
	}
}

func mapPermission(relation *corev1.Relation) *Permission {
	return &Permission{
		Name:    relation.Name,
		UserSet: mapUserSet(relation.GetUsersetRewrite()),
		Comment: getMetadataComments(relation.GetMetadata()),
	}
}

func mapUserSet(userset *corev1.UsersetRewrite) *UserSet {
	union := userset.GetUnion()
	if union != nil {
		return &UserSet{
			Operation: "union",
			Children:  mapUserSetChild(union.GetChild()),
		}
	}

	intersection := userset.GetIntersection()
	if intersection != nil {
		return &UserSet{
			Operation: "intersection",
			Children:  mapUserSetChild(intersection.GetChild()),
		}
	}

	exclusion := userset.GetExclusion()
	if exclusion != nil {
		return &UserSet{
			Operation: "exclusion",
			Children:  mapUserSetChild(exclusion.GetChild()),
		}
	}

	return nil
}

func mapUserSetChild(children []*corev1.SetOperation_Child) []*UserSet {
	var sets []*UserSet
	for _, child := range children {
		computed := child.GetComputedUserset()
		if computed != nil {
			sets = append(sets, &UserSet{
				Relation: computed.Relation,
			})
		}

		tuple := child.GetTupleToUserset()
		if tuple != nil {
			sets = append(sets, &UserSet{
				Relation:   tuple.Tupleset.Relation,
				Permission: tuple.ComputedUserset.Relation,
			})
		}

		set := child.GetUsersetRewrite()
		if set != nil {
			sets = append(sets, mapUserSet(set))
		}
	}
	return sets
}

func mapRelationType(relationType *corev1.AllowedRelation) *RelationType {
	Relation, ok := relationType.RelationOrWildcard.(*corev1.AllowedRelation_Relation)
	var relationName string
	if !ok {
		relationName = "*"
	} else {
		relationName = Relation.Relation
		if relationName == "..." {
			relationName = ""
		}
	}

	caveat := relationType.RequiredCaveat
	var caveatName string
	if caveat != nil {
		caveatName = caveat.CaveatName
	} else {
		caveatName = ""
	}
	return &RelationType{
		Type:     relationType.Namespace,
		Relation: relationName,
		Caveat:   caveatName,
	}
}

func getMetadataComments(metaData *corev1.Metadata) string {
	comment := ``
	for _, d := range metaData.GetMetadataMessage() {
		if d.GetTypeUrl() == `type.googleapis.com/impl.v1.DocComment` {
			comment += string(d.GetValue()[2:]) + "\n"
		}
	}
	return strings.TrimSpace(comment)
}

func mapCaveat(caveat *corev1.CaveatDefinition) *Caveat {
	var parameters []string
	for _, t := range caveat.ParameterTypes {
		parameters = append(parameters, t.TypeName)
	}

	return &Caveat{
		Name:       caveat.Name,
		Parameters: parameters,
		Comment:    getMetadataComments(caveat.Metadata),
	}
}

type Definition struct {
	Name        string        `json:"name"`
	Namespace   string        `json:"namespace,omitempty"`
	Relations   []*Relation   `json:"relations,omitempty"`
	Permissions []*Permission `json:"permissions,omitempty"`
	Comment     string        `json:"comment,omitempty"`
}

type Relation struct {
	Name    string          `json:"name"`
	Types   []*RelationType `json:"types"`
	Comment string          `json:"comment,omitempty"`
}

type RelationType struct {
	Type     string `json:"type"`
	Relation string `json:"relation,omitempty"`
	Caveat   string `json:"caveat,omitempty"`
}

type Permission struct {
	Name    string   `json:"name"`
	UserSet *UserSet `json:"userSet"`
	Comment string   `json:"comment,omitempty"`
}

type UserSet struct {
	Operation  string     `json:"operation,omitempty"`
	Relation   string     `json:"relation,omitempty"`
	Permission string     `json:"permission,omitempty"`
	Children   []*UserSet `json:"children,omitempty"`
}

type Caveat struct {
	Name       string   `json:"name"`
	Parameters []string `json:"parameters"`
	Comment    string   `json:"comment,omitempty"`
}

type Schema struct {
	Definitions []*Definition `json:"definitions"`
	Caveats     []*Caveat     `json:"caveats,omitempty"`
}
