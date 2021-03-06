// -*- Mode: Go; indent-tabs-mode: t -*-

/*
 * Copyright (C) 2015 Canonical Ltd
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License version 3 as
 * published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package asserts

import (
	"fmt"
	"regexp"
	"strconv"
)

type attrMatcher interface {
	match(context string, v interface{}) error
}

func chain(context, k string) string {
	if context == "" {
		return k
	}
	return fmt.Sprintf("%s.%s", context, k)
}

type compileContext struct {
	dotted string
	hadMap bool
	wasAlt bool
}

func (cc compileContext) String() string {
	return cc.dotted
}

func (cc compileContext) keyEntry(k string) compileContext {
	return compileContext{
		dotted: chain(cc.dotted, k),
		hadMap: true,
		wasAlt: false,
	}
}

func (cc compileContext) alt(alt int) compileContext {
	return compileContext{
		dotted: fmt.Sprintf("%s/alt#%d/", cc.dotted, alt+1),
		hadMap: cc.hadMap,
		wasAlt: true,
	}
}

// compileAttrMatcher compiles an attrMatcher derived from constraints,
func compileAttrMatcher(cc compileContext, constraints interface{}) (attrMatcher, error) {
	switch x := constraints.(type) {
	case map[string]interface{}:
		return compileMapAttrMatcher(cc, x)
	case []interface{}:
		if cc.wasAlt {
			return nil, fmt.Errorf("cannot nest alternative constraints directly at %q", cc)
		}
		return compileAltAttrMatcher(cc, x)
	case string:
		if !cc.hadMap {
			return nil, fmt.Errorf("first level of non alternative constraints must be a set of key-value contraints")
		}
		return compileRegexpAttrMatcher(cc, x)
	default:
		return nil, fmt.Errorf("constraint %q must be a key-value map, regexp or a list of alternative constraints: %v", cc, x)
	}
}

type mapAttrMatcher map[string]attrMatcher

func compileMapAttrMatcher(cc compileContext, m map[string]interface{}) (attrMatcher, error) {
	matcher := make(mapAttrMatcher)
	for k, constraint := range m {
		matcher1, err := compileAttrMatcher(cc.keyEntry(k), constraint)
		if err != nil {
			return nil, err
		}
		matcher[k] = matcher1
	}
	return matcher, nil
}

func matchEntry(context, k string, matcher1 attrMatcher, v interface{}) error {
	context = chain(context, k)
	if v == nil {
		return fmt.Errorf("attribute %q has constraints but is unset", context)
	}
	if err := matcher1.match(context, v); err != nil {
		return err
	}
	return nil
}

func matchList(context string, matcher attrMatcher, l []interface{}) error {
	for i, elem := range l {
		if err := matcher.match(chain(context, strconv.Itoa(i)), elem); err != nil {
			return err
		}
	}
	return nil
}

func (matcher mapAttrMatcher) match(context string, v interface{}) error {
	switch x := v.(type) {
	case map[string]interface{}: // top level looks like this
		for k, matcher1 := range matcher {
			if err := matchEntry(context, k, matcher1, x[k]); err != nil {
				return err
			}
		}
	case map[interface{}]interface{}: // nested maps look like this
		for k, matcher1 := range matcher {
			if err := matchEntry(context, k, matcher1, x[k]); err != nil {
				return err
			}
		}
	case []interface{}:
		return matchList(context, matcher, x)
	default:
		return fmt.Errorf("attribute %q must be a map", context)
	}
	return nil
}

type regexpAttrMatcher struct {
	*regexp.Regexp
}

func compileRegexpAttrMatcher(cc compileContext, s string) (attrMatcher, error) {
	rx, err := regexp.Compile("^" + s + "$")
	if err != nil {
		return nil, fmt.Errorf("cannot compile %q constraint %q: %v", cc, s, err)
	}
	return regexpAttrMatcher{rx}, nil
}

func (matcher regexpAttrMatcher) match(context string, v interface{}) error {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case bool:
		s = strconv.FormatBool(x)
	case int:
		s = strconv.Itoa(x)
	case []interface{}:
		return matchList(context, matcher, x)
	default:
		return fmt.Errorf("attribute %q must be a scalar or list", context)
	}
	if !matcher.Regexp.MatchString(s) {
		return fmt.Errorf("attribute %q value %q does not match %v", context, s, matcher.Regexp)
	}
	return nil

}

type altAttrMatcher struct {
	alts []attrMatcher
}

func compileAltAttrMatcher(cc compileContext, l []interface{}) (attrMatcher, error) {
	alts := make([]attrMatcher, len(l))
	for i, constraint := range l {
		matcher1, err := compileAttrMatcher(cc.alt(i), constraint)
		if err != nil {
			return nil, err
		}
		alts[i] = matcher1
	}
	return altAttrMatcher{alts}, nil

}

func (matcher altAttrMatcher) match(context string, v interface{}) error {
	var firstErr error
	for _, alt := range matcher.alts {
		err := alt.match(context, v)
		if err == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	ctxDescr := ""
	if context != "" {
		ctxDescr = fmt.Sprintf(" for attribute %q", context)
	}
	return fmt.Errorf("no alternative%s matches: %v", ctxDescr, firstErr)
}

// AttributeConstraints implements a set of constraints on the attributes of a slot or plug.
type AttributeConstraints struct {
	matcher attrMatcher
}

// compileAttributeConstraints checks and compiles a mapping or list from the assertion format into AttributeConstraints.
func compileAttributeConstraints(constraints interface{}) (*AttributeConstraints, error) {
	matcher, err := compileAttrMatcher(compileContext{}, constraints)
	if err != nil {
		return nil, err
	}
	return &AttributeConstraints{matcher: matcher}, nil
}

// Check checks whether attrs don't match the constraints.
func (c *AttributeConstraints) Check(attrs map[string]interface{}) error {
	return c.matcher.match("", attrs)
}
