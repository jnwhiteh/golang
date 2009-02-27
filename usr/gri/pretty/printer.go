// Copyright 2009 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package Printer

import (
	"os";
	"io";
	"vector";
	"tabwriter";
	"flag";
	"fmt";
	Utils "utils";
	Scanner "scanner";
	AST "ast";
	SymbolTable "symboltable";
)

var (
	debug = flag.Bool("debug", false, "print debugging information");
	def = flag.Bool("def", false, "print 'def' instead of 'const', 'type', 'func' - experimental");

	// layout control
	tabwidth = flag.Int("tabwidth", 8, "tab width");
	usetabs = flag.Bool("usetabs", true, "align with tabs instead of blanks");
	newlines = flag.Bool("newlines", true, "respect newlines in source");
	maxnewlines = flag.Int("maxnewlines", 3, "max. number of consecutive newlines");

	// formatting control
	comments = flag.Bool("comments", true, "print comments");
	optsemicolons = flag.Bool("optsemicolons", false, "print optional semicolons");
)


// ----------------------------------------------------------------------------
// Elementary support

func unimplemented() {
	panic("unimplemented");
}


func unreachable() {
	panic("unreachable");
}


func assert(pred bool) {
	if !pred {
		panic("assertion failed");
	}
}


// ----------------------------------------------------------------------------
// Printer

// Separators - printed in a delayed fashion, depending on context.
const (
	none = iota;
	blank;
	tab;
	comma;
	semicolon;
)


// Semantic states - control formatting.
const (
	normal = iota;
	opening_scope;  // controls indentation, scope level
	closing_scope;  // controls indentation, scope level
	inside_list;  // controls extra line breaks
)


type Printer struct {
	// output
	text io.Write;
	
	// formatting control
	html bool;

	// comments
	comments *vector.Vector;  // the list of all comments
	cindex int;  // the current comments index
	cpos int;  // the position of the next comment

	// current state
	lastpos int;  // pos after last string
	level int;  // scope level
	indentation int;  // indentation level (may be different from scope level)

	// formatting parameters
	opt_semi bool;  // // true if semicolon separator is optional in statement list
	separator int;  // pending separator
	newlines int;  // pending newlines

	// semantic state
	state int;  // current semantic state
	laststate int;  // state for last string
	
	// expression precedence
	prec int;
}


func (P *Printer) HasComment(pos int) bool {
	return *comments && P.cpos < pos;
}


func (P *Printer) NextComment() {
	P.cindex++;
	if P.comments != nil && P.cindex < P.comments.Len() {
		P.cpos = P.comments.At(P.cindex).(*AST.Comment).Pos;
	} else {
		P.cpos = 1<<30;  // infinite
	}
}


func (P *Printer) Init(text io.Write, html bool, comments *vector.Vector) {
	// writers
	P.text = text;
	
	// formatting control
	P.html = html;

	// comments
	P.comments = comments;
	P.cindex = -1;
	P.NextComment();

	// formatting parameters & semantic state initialized correctly by default
	
	// expression precedence
	P.prec = Scanner.LowestPrec;
}


// ----------------------------------------------------------------------------
// Printing support

func (P *Printer) htmlEscape(s string) string {
	if P.html {
		var esc string;
		for i := 0; i < len(s); i++ {
			switch s[i] {
			case '<': esc = "&lt;";
			case '&': esc = "&amp;";
			default: continue;
			}
			return s[0 : i] + esc + P.htmlEscape(s[i+1 : len(s)]);
		}
	}
	return s;
}


// Reduce contiguous sequences of '\t' in a string to a single '\t'.
func untabify(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\t' {
			j := i;
			for j < len(s) && s[j] == '\t' {
				j++;
			}
			if j-i > 1 {  // more then one tab
				return s[0 : i+1] + untabify(s[j : len(s)]);
			}
		}
	}
	return s;
}


func (P *Printer) Printf(format string, s ...) {
	n, err := fmt.Fprintf(P.text, format, s);
	if err != nil {
		panic("print error - exiting");
	}
}


func (P *Printer) Newline(n int) {
	if n > 0 {
		m := int(*maxnewlines);
		if n > m {
			n = m;
		}
		for ; n > 0; n-- {
			P.Printf("\n");
		}
		for i := P.indentation; i > 0; i-- {
			P.Printf("\t");
		}
	}
}


func (P *Printer) TaggedString(pos int, tag, s, endtag string) {
	// use estimate for pos if we don't have one
	if pos == 0 {
		pos = P.lastpos;
	}

	// --------------------------------
	// print pending separator, if any
	// - keep track of white space printed for better comment formatting
	// TODO print white space separators after potential comments and newlines
	// (currently, we may get trailing white space before a newline)
	trailing_char := 0;
	switch P.separator {
	case none:	// nothing to do
	case blank:
		P.Printf(" ");
		trailing_char = ' ';
	case tab:
		P.Printf("\t");
		trailing_char = '\t';
	case comma:
		P.Printf(",");
		if P.newlines == 0 {
			P.Printf(" ");
			trailing_char = ' ';
		}
	case semicolon:
		if P.level > 0 {	// no semicolons at level 0
			P.Printf(";");
			if P.newlines == 0 {
				P.Printf(" ");
				trailing_char = ' ';
			}
		}
	default:	panic("UNREACHABLE");
	}
	P.separator = none;

	// --------------------------------
	// interleave comments, if any
	nlcount := 0;
	for ; P.HasComment(pos); P.NextComment() {
		// we have a comment/newline that comes before the string
		comment := P.comments.At(P.cindex).(*AST.Comment);
		ctext := comment.Text;

		if ctext == "\n" {
			// found a newline in src - count it
			nlcount++;

		} else {
			// classify comment (len(ctext) >= 2)
			//-style comment
			if nlcount > 0 || P.cpos == 0 {
				// only white space before comment on this line
				// or file starts with comment
				// - indent
				if !*newlines && P.cpos != 0 {
					nlcount = 1;
				}
				P.Newline(nlcount);
				nlcount = 0;

			} else {
				// black space before comment on this line
				if ctext[1] == '/' {
					//-style comment
					// - put in next cell unless a scope was just opened
					//   in which case we print 2 blanks (otherwise the
					//   entire scope gets indented like the next cell)
					if P.laststate == opening_scope {
						switch trailing_char {
						case ' ': P.Printf(" ");  // one space already printed
						case '\t': // do nothing
						default: P.Printf("  ");
						}
					} else {
						if trailing_char != '\t' {
							P.Printf("\t");
						}
					}
				} else {
					/*-style comment */
					// - print surrounded by blanks
					if trailing_char == 0 {
						P.Printf(" ");
					}
					ctext += " ";
				}
			}

			// print comment
			if *debug {
				P.Printf("[%d]", P.cpos);
			}
			// calling untabify increases the change for idempotent output
			// since tabs in comments are also interpreted by tabwriter
			P.Printf("%s", P.htmlEscape(untabify(ctext)));

			if ctext[1] == '/' {
				//-style comments must end in newline
				if P.newlines == 0 {  // don't add newlines if not needed
					P.newlines = 1;
				}
			}
		}
	}
	// At this point we may have nlcount > 0: In this case we found newlines
	// that were not followed by a comment. They are recognized (or not) when
	// printing newlines below.

	// --------------------------------
	// interpret state
	// (any pending separator or comment must be printed in previous state)
	switch P.state {
	case normal:
	case opening_scope:
	case closing_scope:
		P.indentation--;
	case inside_list:
	default:
		panic("UNREACHABLE");
	}

	// --------------------------------
	// print pending newlines
	if *newlines && (P.newlines > 0 || P.state == inside_list) && nlcount > P.newlines {
		// Respect additional newlines in the source, but only if we
		// enabled this feature (newlines.BVal()) and we are expecting
		// newlines (P.newlines > 0 || P.state == inside_list).
		// Otherwise - because we don't have all token positions - we
		// get funny formatting.
		P.newlines = nlcount;
	}
	nlcount = 0;
	P.Newline(P.newlines);
	P.newlines = 0;

	// --------------------------------
	// print string
	if *debug {
		P.Printf("[%d]", pos);
	}
	P.Printf("%s%s%s", tag, P.htmlEscape(s), endtag);

	// --------------------------------
	// interpret state
	switch P.state {
	case normal:
	case opening_scope:
		P.level++;
		P.indentation++;
	case closing_scope:
		P.level--;
	case inside_list:
	default:
		panic("UNREACHABLE");
	}
	P.laststate = P.state;
	P.state = none;

	// --------------------------------
	// done
	P.opt_semi = false;
	P.lastpos = pos + len(s);  // rough estimate
}


func (P *Printer) String(pos int, s string) {
	P.TaggedString(pos, "", s, "");
}


func (P *Printer) Token(pos int, tok int) {
	P.String(pos, Scanner.TokenString(tok));
	//P.TaggedString(pos, "<b>", Scanner.TokenString(tok), "</b>");
}


func (P *Printer) Error(pos int, tok int, msg string) {
	fmt.Printf("\ninternal printing error: pos = %d, tok = %s, %s\n", pos, Scanner.TokenString(tok), msg);
	panic();
}


// ----------------------------------------------------------------------------
// HTML support

func (P *Printer) HtmlPrologue(title string) {
	if P.html {
		P.TaggedString(0,
			"<html>\n"
			"<head>\n"
			"	<META HTTP-EQUIV=\"Content-Type\" CONTENT=\"text/html; charset=UTF-8\">\n"
			"	<title>" + P.htmlEscape(title) + "</title>\n"
			"	<style type=\"text/css\">\n"
			"	</style>\n"
			"</head>\n"
			"<body>\n"
			"<pre>\n",
			"", ""
		)
	}
}


func (P *Printer) HtmlEpilogue() {
	if P.html {
		P.TaggedString(0,
			"</pre>\n"
			"</body>\n"
			"<html>\n",
			"", ""
		)
	}
}


func (P *Printer) HtmlIdentifier(x *AST.Ident) {
	obj := x.Obj;
	if P.html && obj.Kind != SymbolTable.NONE {
		// depending on whether we have a declaration or use, generate different html
		// - no need to htmlEscape ident
		id := Utils.IntToString(obj.Id, 10);
		if x.Pos_ == obj.Pos {
			// probably the declaration of x
			P.TaggedString(x.Pos_, `<a name="id` + id + `">`, obj.Ident, `</a>`);
		} else {
			// probably not the declaration of x
			P.TaggedString(x.Pos_, `<a href="#id` + id + `">`, obj.Ident, `</a>`);
		}
	} else {
		P.String(x.Pos_, obj.Ident);
	}
}


func (P *Printer) HtmlPackageName(pos int, name string) {
	if P.html {
		sname := name[1 : len(name)-1];  // strip quotes  TODO do this elsewhere eventually
		// TODO CAPITAL HACK BELOW FIX THIS
		P.TaggedString(pos, `"<a href="/src/lib/` + sname + `.go">`, sname, `</a>"`);
	} else {
		P.String(pos, name);
	}
}


// ----------------------------------------------------------------------------
// Support

func (P *Printer) Expr(x AST.Expr)

func (P *Printer) Idents(list []*AST.Ident) {
	for i, x := range list {
		if i > 0 {
			P.Token(0, Scanner.COMMA);
			P.separator = blank;
			P.state = inside_list;
		}
		P.Expr(x);
	}
}


func (P *Printer) Parameters(list []*AST.Field) {
	P.Token(0, Scanner.LPAREN);
	if len(list) > 0 {
		for i, par := range list {
			if i > 0 {
				P.separator = comma;
			}
			if len(par.Idents) > 0 {
				P.Idents(par.Idents);
				P.separator = blank
			};
			P.Expr(par.Typ);
		}
	}
	P.Token(0, Scanner.RPAREN);
}


// Returns the separator (semicolon or none) required if
// the type is terminating a declaration or statement.
func (P *Printer) Signature(sig *AST.Signature) {
	P.Parameters(sig.Params);
	if sig.Result != nil {
		P.separator = blank;

		if len(sig.Result) == 1 && sig.Result[0].Idents == nil {
			// single anonymous result
			// => no parentheses needed unless it's a function type
			fld := sig.Result[0];
			if dummy, is_ftyp := fld.Typ.(*AST.FunctionType); !is_ftyp {
				P.Expr(fld.Typ);
				return;
			}
		}
		
		P.Parameters(sig.Result);
	}
}


func (P *Printer) Fields(list []*AST.Field, end int, is_interface bool) {
	P.state = opening_scope;
	P.separator = blank;
	P.Token(0, Scanner.LBRACE);

	if len(list) > 0 {
		P.newlines = 1;
		for i, fld := range list {
			if i > 0 {
				P.separator = semicolon;
				P.newlines = 1;
			}
			if len(fld.Idents) > 0 {
				P.Idents(fld.Idents);
				P.separator = tab
			};
			if is_interface {
				if ftyp, is_ftyp := fld.Typ.(*AST.FunctionType); is_ftyp {
					P.Signature(ftyp.Sig);
				} else {
					P.Expr(fld.Typ);
				}
			} else {
				P.Expr(fld.Typ);
				if fld.Tag != nil {
					P.separator = tab;
					P.Expr(fld.Tag);
				}
			}
		}
		P.newlines = 1;
	}

	P.state = closing_scope;
	P.Token(end, Scanner.RBRACE);
	P.opt_semi = true;
}


// ----------------------------------------------------------------------------
// Expressions

func (P *Printer) Block(b *AST.Block, indent bool)
func (P *Printer) Expr1(x AST.Expr, prec1 int)


func (P *Printer) DoBadExpr(x *AST.BadExpr) {
	P.String(0, "BadExpr");
}


func (P *Printer) DoIdent(x *AST.Ident) {
	P.HtmlIdentifier(x);
}


func (P *Printer) DoBinaryExpr(x *AST.BinaryExpr) {
	if x.Tok == Scanner.COMMA {
		// (don't use binary expression printing because of different spacing)
		P.Expr(x.X);
		P.Token(x.Pos_, Scanner.COMMA);
		P.separator = blank;
		P.state = inside_list;
		P.Expr(x.Y);
	} else {
		prec := Scanner.Precedence(x.Tok);
		if prec < P.prec {
			P.Token(0, Scanner.LPAREN);
		}
		P.Expr1(x.X, prec);
		P.separator = blank;
		P.Token(x.Pos_, x.Tok);
		P.separator = blank;
		P.Expr1(x.Y, prec);
		if prec < P.prec {
			P.Token(0, Scanner.RPAREN);
		}
	}
}


func (P *Printer) DoUnaryExpr(x *AST.UnaryExpr) {
	prec := Scanner.UnaryPrec;
	if prec < P.prec {
		P.Token(0, Scanner.LPAREN);
	}
	P.Token(x.Pos_, x.Tok);
	if x.Tok == Scanner.RANGE {
		P.separator = blank;
	}
	P.Expr1(x.X, prec);
	if prec < P.prec {
		P.Token(0, Scanner.RPAREN);
	}
}


func (P *Printer) DoBasicLit(x *AST.BasicLit) {
	P.String(x.Pos_, x.Val);
}


func (P *Printer) DoFunctionLit(x *AST.FunctionLit) {
	P.Token(x.Pos_, Scanner.FUNC);
	P.Signature(x.Typ);
	P.separator = blank;
	P.Block(x.Body, true);
	P.newlines = 0;
}


func (P *Printer) DoGroup(x *AST.Group) {
	P.Token(x.Pos_, Scanner.LPAREN);
	P.Expr(x.X);
	P.Token(0, Scanner.RPAREN);
}


func (P *Printer) DoSelector(x *AST.Selector) {
	P.Expr1(x.X, Scanner.HighestPrec);
	P.Token(x.Pos_, Scanner.PERIOD);
	P.Expr1(x.Sel, Scanner.HighestPrec);
}


func (P *Printer) DoTypeGuard(x *AST.TypeGuard) {
	P.Expr1(x.X, Scanner.HighestPrec);
	P.Token(x.Pos_, Scanner.PERIOD);
	P.Token(0, Scanner.LPAREN);
	P.Expr(x.Typ);
	P.Token(0, Scanner.RPAREN);
}


func (P *Printer) DoIndex(x *AST.Index) {
	P.Expr1(x.X, Scanner.HighestPrec);
	P.Token(x.Pos_, Scanner.LBRACK);
	P.Expr1(x.I, 0);
	P.Token(0, Scanner.RBRACK);
}


func (P *Printer) DoCall(x *AST.Call) {
	P.Expr1(x.F, Scanner.HighestPrec);
	P.Token(x.Pos_, Scanner.LPAREN);
	P.Expr(x.Args);
	P.Token(0, Scanner.RPAREN);
}


func (P *Printer) DoEllipsis(x *AST.Ellipsis) {
	P.Token(x.Pos_, Scanner.ELLIPSIS);
}


func (P *Printer) DoArrayType(x *AST.ArrayType) {
	P.Token(x.Pos_, Scanner.LBRACK);
	if x.Len != nil {
		P.Expr(x.Len);
	}
	P.Token(0, Scanner.RBRACK);
	P.Expr(x.Elt);
}


func (P *Printer) DoStructType(x *AST.StructType) {
	P.Token(x.Pos_, Scanner.STRUCT);
	if x.End > 0 {
		P.Fields(x.Fields, x.End, false);
	}
}


func (P *Printer) DoPointerType(x *AST.PointerType) {
	P.Token(x.Pos_, Scanner.MUL);
	P.Expr(x.Base);
}


func (P *Printer) DoFunctionType(x *AST.FunctionType) {
	P.Token(x.Pos_, Scanner.FUNC);
	P.Signature(x.Sig);
}


func (P *Printer) DoInterfaceType(x *AST.InterfaceType) {
	P.Token(x.Pos_, Scanner.INTERFACE);
	if x.End > 0 {
		P.Fields(x.Methods, x.End, true);
	}
}


func (P *Printer) DoSliceType(x *AST.SliceType) {
	unimplemented();
}


func (P *Printer) DoMapType(x *AST.MapType) {
	P.Token(x.Pos_, Scanner.MAP);
	P.separator = blank;
	P.Token(0, Scanner.LBRACK);
	P.Expr(x.Key);
	P.Token(0, Scanner.RBRACK);
	P.Expr(x.Val);
}


func (P *Printer) DoChannelType(x *AST.ChannelType) {
	switch x.Mode {
	case AST.FULL:
		P.Token(x.Pos_, Scanner.CHAN);
	case AST.RECV:
		P.Token(x.Pos_, Scanner.ARROW);
		P.Token(0, Scanner.CHAN);
	case AST.SEND:
		P.Token(x.Pos_, Scanner.CHAN);
		P.separator = blank;
		P.Token(0, Scanner.ARROW);
	}
	P.separator = blank;
	P.Expr(x.Val);
}


func (P *Printer) Expr1(x AST.Expr, prec1 int) {
	if x == nil {
		return;  // empty expression list
	}

	saved_prec := P.prec;
	P.prec = prec1;
	x.Visit(P);
	P.prec = saved_prec;
}


func (P *Printer) Expr(x AST.Expr) {
	P.Expr1(x, Scanner.LowestPrec);
}


// ----------------------------------------------------------------------------
// Statements

func (P *Printer) Stat(s AST.Stat) {
	s.Visit(P);
}


func (P *Printer) StatementList(list *vector.Vector) {
	for i := 0; i < list.Len(); i++ {
		if i == 0 {
			P.newlines = 1;
		} else {  // i > 0
			if !P.opt_semi {
				// semicolon is required
				P.separator = semicolon;
			}
		}
		P.Stat(list.At(i).(AST.Stat));
		P.newlines = 1;
		P.state = inside_list;
	}
}


func (P *Printer) Block(b *AST.Block, indent bool) {
	P.state = opening_scope;
	P.Token(b.Pos, b.Tok);
	if !indent {
		P.indentation--;
	}
	P.StatementList(b.List);
	if !indent {
		P.indentation++;
	}
	if !*optsemicolons {
		P.separator = none;
	}
	P.state = closing_scope;
	if b.Tok == Scanner.LBRACE {
		P.Token(b.End, Scanner.RBRACE);
		P.opt_semi = true;
	} else {
		P.String(0, "");  // process closing_scope state transition!
	}
}


func (P *Printer) Decl(d AST.Decl);

func (P *Printer) DoBadStat(s *AST.BadStat) {
	panic();
}


func (P *Printer) DoLabelDecl(s *AST.LabelDecl) {
	P.indentation--;
	P.Expr(s.Label);
	P.Token(s.Pos, Scanner.COLON);
	P.indentation++;
}


func (P *Printer) DoDeclarationStat(s *AST.DeclarationStat) {
	P.Decl(s.Decl);
}


func (P *Printer) DoExpressionStat(s *AST.ExpressionStat) {
	switch s.Tok {
	case Scanner.ILLEGAL:
		P.Expr(s.Expr);
	case Scanner.INC, Scanner.DEC:
		P.Expr(s.Expr);
		P.Token(s.Pos, s.Tok);
	case Scanner.RETURN, Scanner.GO, Scanner.DEFER:
		P.Token(s.Pos, s.Tok);
		if s.Expr != nil {
			P.separator = blank;
			P.Expr(s.Expr);
		}
	default:
		P.Error(s.Pos, s.Tok, "DoExpressionStat");
		unreachable();
	}
}


func (P *Printer) DoCompositeStat(s *AST.CompositeStat) {
	P.Block(s.Body, true);
}


func (P *Printer) ControlClause(isForStat bool, init AST.Stat, expr AST.Expr, post AST.Stat) {
	P.separator = blank;
	if init == nil && post == nil {
		// no semicolons required
		if expr != nil {
			P.Expr(expr);
		}
	} else {
		// all semicolons required
		// (they are not separators, print them explicitly)
		if init != nil {
			P.Stat(init);
			P.separator = none;
		}
		P.Token(0, Scanner.SEMICOLON);
		P.separator = blank;
		if expr != nil {
			P.Expr(expr);
			P.separator = none;
		}
		if isForStat {
			P.Token(0, Scanner.SEMICOLON);
			P.separator = blank;
			if post != nil {
				P.Stat(post);
			}
		}
	}
	P.separator = blank;
}


func (P *Printer) DoIfStat(s *AST.IfStat) {
	P.Token(s.Pos, Scanner.IF);
	P.ControlClause(false, s.Init, s.Cond, nil);
	P.Block(s.Body, true);
	if s.Else != nil {
		P.separator = blank;
		P.Token(0, Scanner.ELSE);
		P.separator = blank;
		P.Stat(s.Else);
	}
}


func (P *Printer) DoForStat(s *AST.ForStat) {
	P.Token(s.Pos, Scanner.FOR);
	P.ControlClause(true, s.Init, s.Cond, s.Post);
	P.Block(s.Body, true);
}


func (P *Printer) DoCaseClause(s *AST.CaseClause) {
	if s.Expr != nil {
		P.Token(s.Pos, Scanner.CASE);
		P.separator = blank;
		P.Expr(s.Expr);
	} else {
		P.Token(s.Pos, Scanner.DEFAULT);
	}
	// TODO: try to use P.Block instead
	// P.Block(s.Body, true);
	P.Token(s.Body.Pos, Scanner.COLON);
	P.indentation++;
	P.StatementList(s.Body.List);
	P.indentation--;
	P.newlines = 1;
}


func (P *Printer) DoSwitchStat(s *AST.SwitchStat) {
	P.Token(s.Pos, Scanner.SWITCH);
	P.ControlClause(false, s.Init, s.Tag, nil);
	P.Block(s.Body, false);
}


func (P *Printer) DoSelectStat(s *AST.SelectStat) {
	P.Token(s.Pos, Scanner.SELECT);
	P.separator = blank;
	P.Block(s.Body, false);
}


func (P *Printer) DoControlFlowStat(s *AST.ControlFlowStat) {
	P.Token(s.Pos, s.Tok);
	if s.Label != nil {
		P.separator = blank;
		P.Expr(s.Label);
	}
}


func (P *Printer) DoEmptyStat(s *AST.EmptyStat) {
	P.String(s.Pos, "");
}


// ----------------------------------------------------------------------------
// Declarations

func (P *Printer) DoBadDecl(d *AST.BadDecl) {
	unimplemented();
}


func (P *Printer) DoImportDecl(d *AST.ImportDecl) {
	if d.Pos > 0 {
		P.Token(d.Pos, Scanner.IMPORT);
		P.separator = blank;
	}
	if d.Ident != nil {
		P.Expr(d.Ident);
	} else {
		P.String(d.Path.Pos(), "");  // flush pending ';' separator/newlines
	}
	P.separator = tab;
	if lit, is_lit := d.Path.(*AST.BasicLit); is_lit && lit.Tok == Scanner.STRING {
		P.HtmlPackageName(lit.Pos_, lit.Val);
	} else {
		// we should only reach here for strange imports
		// import "foo" "bar"
		P.Expr(d.Path);
	}
	P.newlines = 2;
}


func (P *Printer) DoConstDecl(d *AST.ConstDecl) {
	if d.Pos > 0 {
		P.Token(d.Pos, Scanner.CONST);
		P.separator = blank;
	}
	P.Idents(d.Idents);
	if d.Typ != nil {
		P.separator = blank;  // TODO switch to tab? (indentation problem with structs)
		P.Expr(d.Typ);
	}
	if d.Vals != nil {
		P.separator = tab;
		P.Token(0, Scanner.ASSIGN);
		P.separator = blank;
		P.Expr(d.Vals);
	}
	P.newlines = 2;
}


func (P *Printer) DoTypeDecl(d *AST.TypeDecl) {
	if d.Pos > 0 {
		P.Token(d.Pos, Scanner.TYPE);
		P.separator = blank;
	}
	P.Expr(d.Ident);
	P.separator = blank;  // TODO switch to tab? (but indentation problem with structs)
	P.Expr(d.Typ);
	P.newlines = 2;
}


func (P *Printer) DoVarDecl(d *AST.VarDecl) {
	if d.Pos > 0 {
		P.Token(d.Pos, Scanner.VAR);
		P.separator = blank;
	}
	P.Idents(d.Idents);
	if d.Typ != nil {
		P.separator = blank;  // TODO switch to tab? (indentation problem with structs)
		P.Expr(d.Typ);
		//P.separator = P.Type(d.Typ);
	}
	if d.Vals != nil {
		P.separator = tab;
		P.Token(0, Scanner.ASSIGN);
		P.separator = blank;
		P.Expr(d.Vals);
	}
	P.newlines = 2;
}


func (P *Printer) DoFuncDecl(d *AST.FuncDecl) {
	P.Token(d.Pos_, Scanner.FUNC);
	P.separator = blank;
	if recv := d.Recv; recv != nil {
		// method: print receiver
		P.Token(0, Scanner.LPAREN);
		if len(recv.Idents) > 0 {
			P.Expr(recv.Idents[0]);
			P.separator = blank;
		}
		P.Expr(recv.Typ);
		P.Token(0, Scanner.RPAREN);
		P.separator = blank;
	}
	P.Expr(d.Ident);
	P.Signature(d.Sig);
	if d.Body != nil {
		P.separator = blank;
		P.Block(d.Body, true);
	}
	P.newlines = 2;
}


func (P *Printer) DoDeclList(d *AST.DeclList) {
	if !*def || d.Tok == Scanner.IMPORT || d.Tok == Scanner.VAR {
		P.Token(d.Pos, d.Tok);
	} else {
		P.String(d.Pos, "def");
	}
	P.separator = blank;

	// group of parenthesized declarations
	P.state = opening_scope;
	P.Token(0, Scanner.LPAREN);
	if len(d.List) > 0 {
		P.newlines = 1;
		for i := 0; i < len(d.List); i++ {
			if i > 0 {
				P.separator = semicolon;
			}
			P.Decl(d.List[i]);
			P.newlines = 1;
		}
	}
	P.state = closing_scope;
	P.Token(d.End, Scanner.RPAREN);
	P.opt_semi = true;
	P.newlines = 2;
}


func (P *Printer) Decl(d AST.Decl) {
	d.Visit(P);
}


// ----------------------------------------------------------------------------
// Program

func (P *Printer) Program(p *AST.Program) {
	P.Token(p.Pos, Scanner.PACKAGE);
	P.separator = blank;
	P.Expr(p.Ident);
	P.newlines = 1;
	for i := 0; i < len(p.Decls); i++ {
		P.Decl(p.Decls[i]);
	}
	P.newlines = 1;
}


// ----------------------------------------------------------------------------
// External interface

func Print(writer io.Write, html bool, prog *AST.Program) {
	// setup
	var P Printer;
	padchar := byte(' ');
	if *usetabs {
		padchar = '\t';
	}
	text := tabwriter.New(writer, *tabwidth, 1, padchar, true, html);
	P.Init(text, html, prog.Comments);

	// TODO would be better to make the name of the src file be the title
	P.HtmlPrologue("package " + prog.Ident.(*AST.Ident).Obj.Ident);
	P.Program(prog);
	P.HtmlEpilogue();

	P.String(0, "");  // flush pending separator/newlines
	err := text.Flush();
	if err != nil {
		panic("print error - exiting");
	}
}
