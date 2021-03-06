package executor

import (
	"encoding/json"
	"fmt"
	"github.com/chris-ramon/graphql-go/errors"
	"github.com/chris-ramon/graphql-go/language/ast"
	"github.com/chris-ramon/graphql-go/language/kinds"
	"github.com/chris-ramon/graphql-go/language/printer"
	"github.com/chris-ramon/graphql-go/types"
	"math"
	"reflect"
)

// Prepares an object map of variableValues of the correct type based on the
// provided variable definitions and arbitrary input. If the input cannot be
// parsed to match the variable definitions, a GraphQLError will be returned.
func getVariableValues(schema types.GraphQLSchema, definitionASTs []*ast.VariableDefinition, inputs map[string]interface{}) (map[string]interface{}, error) {
	values := map[string]interface{}{}
	for _, defAST := range definitionASTs {
		if defAST == nil || defAST.Variable == nil || defAST.Variable.Name == nil {
			continue
		}
		varName := defAST.Variable.Name.Value
		varValue, err := getVariableValue(schema, defAST, inputs[varName])
		if err != nil {
			return values, err
		}
		values[varName] = varValue
	}
	return values, nil
}

// Prepares an object map of argument values given a list of argument
// definitions and list of argument AST nodes.
func getArgumentValues(argDefs []*types.GraphQLArgument, argASTs []*ast.Argument, variableVariables map[string]interface{}) (map[string]interface{}, error) {

	argASTMap := map[string]*ast.Argument{}
	for _, argAST := range argASTs {
		if argAST.Name != nil {
			argASTMap[argAST.Name.Value] = argAST
		}
	}
	results := map[string]interface{}{}
	for _, argDef := range argDefs {

		name := argDef.Name
		var valueAST ast.Value
		if argAST, ok := argASTMap[name]; ok {
			valueAST = argAST.Value
		}
		value := valueFromAST(valueAST, argDef.Type, variableVariables)
		if isNullish(value) {
			value = argDef.DefaultValue
		}
		if !isNullish(value) {
			results[name] = value
		}
	}
	return results, nil
}

// Given a variable definition, and any value of input, return a value which
// adheres to the variable definition, or throw an error.
func getVariableValue(schema types.GraphQLSchema, definitionAST *ast.VariableDefinition, input interface{}) (interface{}, error) {
	ttype, err := typeFromAST(schema, definitionAST.Type)
	if err != nil {
		return nil, err
	}
	variable := definitionAST.Variable

	if ttype == nil || !types.IsInputType(ttype) {
		return "", graphqlerrors.NewGraphQLError(
			fmt.Sprintf(`Variable "$%v" expected value of type `+
				`"%v" which cannot be used as an input type.`, variable.Name.Value, printer.Print(definitionAST.Type)),
			[]ast.Node{definitionAST},
			"",
			nil,
			[]int{},
		)
	}

	if isValidInputValue(input, ttype) {
		if isNullish(input) {
			defaultValue := definitionAST.DefaultValue
			if defaultValue != nil {
				variables := map[string]interface{}{}
				val := valueFromAST(defaultValue, ttype, variables)
				return val, nil
			}
		}
		return coerceValue(ttype, input), nil
	}
	if isNullish(input) {
		return "", graphqlerrors.NewGraphQLError(
			fmt.Sprintf(`Variable "$%v" of required type `+
				`"%v" was not provided.`, variable.Name.Value, printer.Print(definitionAST.Type)),
			[]ast.Node{definitionAST},
			"",
			nil,
			[]int{},
		)
	}
	inputStr := ""
	b, err := json.Marshal(input)
	if err == nil {
		inputStr = string(b)
	}
	return "", graphqlerrors.NewGraphQLError(
		fmt.Sprintf(`Variable "$%v" expected value of type `+
			`"%v" but got: %v.`, variable.Name.Value, printer.Print(definitionAST.Type), inputStr),
		[]ast.Node{definitionAST},
		"",
		nil,
		[]int{},
	)
}

// Given a type and any value, return a runtime value coerced to match the type.
func coerceValue(ttype types.GraphQLInputType, value interface{}) interface{} {
	if ttype, ok := ttype.(*types.GraphQLNonNull); ok {
		return coerceValue(ttype.OfType, value)
	}
	if isNullish(value) {
		return nil
	}
	if ttype, ok := ttype.(*types.GraphQLList); ok {
		itemType := ttype.OfType
		valType := reflect.ValueOf(value)
		if valType.Kind() == reflect.Slice {
			values := []interface{}{}
			for i := 0; i < valType.Len(); i++ {
				val := valType.Index(i).Interface()
				v := coerceValue(itemType, val)
				values = append(values, v)
			}
			return values
		}
		val := coerceValue(itemType, value)
		return []interface{}{val}
	}
	if ttype, ok := ttype.(*types.GraphQLInputObjectType); ok {

		valueMap, ok := value.(map[string]interface{})
		if !ok {
			valueMap = map[string]interface{}{}
		}

		obj := map[string]interface{}{}
		for fieldName, field := range ttype.GetFields() {
			value, _ := valueMap[fieldName]
			fieldValue := coerceValue(field.Type, value)
			if isNullish(fieldValue) {
				fieldValue = field.DefaultValue
			}
			if !isNullish(fieldValue) {
				obj[fieldName] = fieldValue
			}
		}
		return obj
	}

	switch ttype := ttype.(type) {
	case *types.GraphQLScalarType:
		parsed := ttype.ParseValue(value)
		if !isNullish(parsed) {
			return parsed
		}
	case *types.GraphQLEnumType:
		parsed := ttype.ParseValue(value)
		if !isNullish(parsed) {
			return parsed
		}
	}
	return nil
}

// graphql-js/src/utilities.js`
// TODO: figure out where to organize utils

func typeFromAST(schema types.GraphQLSchema, inputTypeAST ast.Type) (types.GraphQLType, error) {
	switch inputTypeAST := inputTypeAST.(type) {
	case *ast.ListType:
		innerType, err := typeFromAST(schema, inputTypeAST.Type)
		if err != nil {
			return nil, err
		}
		return types.NewGraphQLList(innerType), nil
	case *ast.NonNullType:
		innerType, err := typeFromAST(schema, inputTypeAST.Type)
		if err != nil {
			return nil, err
		}
		return types.NewGraphQLNonNull(innerType), nil
	case *ast.NamedType:
		nameValue := ""
		if inputTypeAST.Name != nil {
			nameValue = inputTypeAST.Name.Value
		}
		ttype := schema.GetType(nameValue)
		return ttype, nil
	default:
		return nil, invariant(inputTypeAST.GetKind() == kinds.NamedType, "Must be a named type.")
	}
}

// isValidInputValue alias isValidJSValue
// Given a value and a GraphQL type, determine if the value will be
// accepted for that type. This is primarily useful for validating the
// runtime values of query variables.
func isValidInputValue(value interface{}, ttype types.GraphQLInputType) bool {
	if ttype, ok := ttype.(*types.GraphQLNonNull); ok {
		if isNullish(value) {
			return false
		}
		return isValidInputValue(value, ttype.OfType)
	}

	if isNullish(value) {
		return true
	}

	switch ttype := ttype.(type) {
	case *types.GraphQLList:
		itemType := ttype.OfType
		valType := reflect.ValueOf(value)
		if valType.Kind() == reflect.Ptr {
			valType = valType.Elem()
		}
		if valType.Kind() == reflect.Slice {
			for i := 0; i < valType.Len(); i++ {
				val := valType.Index(i).Interface()
				if !isValidInputValue(val, itemType) {
					return false
				}
			}
			return true
		}
		return isValidInputValue(value, itemType)

	case *types.GraphQLInputObjectType:
		valueMap, ok := value.(map[string]interface{})
		if !ok {
			return false
		}
		fields := ttype.GetFields()

		// Ensure every provided field is defined.
		for fieldName, _ := range valueMap {
			if _, ok := fields[fieldName]; !ok {
				return false
			}
		}
		// Ensure every defined field is valid.
		for fieldName, _ := range fields {
			isValid := isValidInputValue(valueMap[fieldName], fields[fieldName].Type)
			if !isValid {
				return false
			}
		}
		return true
	}

	switch ttype := ttype.(type) {
	case *types.GraphQLScalarType:
		parsedVal := ttype.ParseValue(value)
		return !isNullish(parsedVal)
	case *types.GraphQLEnumType:
		parsedVal := ttype.ParseValue(value)
		return !isNullish(parsedVal)
	}
	return false
}

// Returns true if a value is null, undefined, or NaN.
func isNullish(value interface{}) bool {
	if value, ok := value.(string); ok {
		return value == ""
	}
	if value, ok := value.(int); ok {
		return math.IsNaN(float64(value))
	}
	if value, ok := value.(float32); ok {
		return math.IsNaN(float64(value))
	}
	if value, ok := value.(float64); ok {
		return math.IsNaN(value)
	}
	return value == nil
}

/**
 * Produces a value given a GraphQL Value AST.
 *
 * A GraphQL type must be provided, which will be used to interpret different
 * GraphQL Value literals.
 *
 * | GraphQL Value        | JSON Value    |
 * | -------------------- | ------------- |
 * | Input Object         | Object        |
 * | List                 | Array         |
 * | Boolean              | Boolean       |
 * | String / Enum Value  | String        |
 * | Int / Float          | Number        |
 *
 */
func valueFromAST(valueAST ast.Value, ttype types.GraphQLInputType, variables map[string]interface{}) interface{} {

	if ttype, ok := ttype.(*types.GraphQLNonNull); ok {
		val := valueFromAST(valueAST, ttype.OfType, variables)
		return val
	}

	if valueAST == nil {
		return nil
	}

	if valueAST, ok := valueAST.(*ast.Variable); ok && valueAST.Kind == kinds.Variable {
		if valueAST.Name == nil {
			return nil
		}
		if variables == nil {
			return nil
		}
		variableName := valueAST.Name.Value
		variableVal, ok := variables[variableName]
		if !ok {
			return nil
		}
		// Note: we're not doing any checking that this variable is correct. We're
		// assuming that this query has been validated and the variable usage here
		// is of the correct type.
		return variableVal
	}

	if ttype, ok := ttype.(*types.GraphQLList); ok {
		itemType := ttype.OfType
		if valueAST, ok := valueAST.(*ast.ListValue); ok && valueAST.Kind == kinds.ListValue {
			values := []interface{}{}
			for _, itemAST := range valueAST.Values {
				v := valueFromAST(itemAST, itemType, variables)
				values = append(values, v)
			}
			return values
		}
		v := valueFromAST(valueAST, itemType, variables)
		return []interface{}{v}
	}

	if ttype, ok := ttype.(*types.GraphQLInputObjectType); ok {
		valueAST, ok := valueAST.(*ast.ObjectValue)
		if !ok {
			return nil
		}
		fieldASTs := map[string]*ast.ObjectField{}
		for _, fieldAST := range valueAST.Fields {
			if fieldAST.Name == nil {
				continue
			}
			fieldName := fieldAST.Name.Value
			fieldASTs[fieldName] = fieldAST

		}
		obj := map[string]interface{}{}
		for fieldName, field := range ttype.GetFields() {
			fieldAST, ok := fieldASTs[fieldName]
			if !ok || fieldAST == nil {
				continue
			}
			fieldValue := valueFromAST(fieldAST.Value, field.Type, variables)
			if isNullish(fieldValue) {
				fieldValue = field.DefaultValue
			}
			if !isNullish(fieldValue) {
				obj[fieldName] = fieldValue
			}
		}
		return obj
	}

	switch ttype := ttype.(type) {
	case *types.GraphQLScalarType:
		parsed := ttype.ParseLiteral(valueAST)
		if !isNullish(parsed) {
			return parsed
		}
	case *types.GraphQLEnumType:
		parsed := ttype.ParseLiteral(valueAST)
		if !isNullish(parsed) {
			return parsed
		}
	}
	return nil
}

func invariant(condition bool, message string) error {
	if !condition {
		return graphqlerrors.NewGraphQLFormattedError(message)
	}
	return nil
}
