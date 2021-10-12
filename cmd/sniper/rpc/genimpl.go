package rpc

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"text/template"

	"github.com/dave/dst"
	"github.com/dave/dst/decorator"
)

func genOrUpdateServer() {
	if !fileExists(serverFile) {
		tpl := &srvTpl{
			Server:  server,
			Version: version,
			Service: upper1st(service),
		}

		save(serverFile, tpl)
	}

	p := fmt.Sprintf("rpc/%s/v%s/%s.twirp.go", server, version, service)
	f, _ := parseAST(p)
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}

		appendFuncs(gd)
		updateComments(gd)

		return // 只处理第一个服务
	}
}

func updateComments(d *ast.GenDecl) {
	comments := getComments(d)

	fset := token.NewFileSet()
	f, err := decorator.ParseFile(fset, serverFile, nil, parser.ParseComments)
	if err != nil {
		return
	}

	decls := make([]dst.Decl, 0, len(f.Decls))
	for _, decl := range f.Decls {
		decls = append(decls, decl)

		switch d := decl.(type) {
		case *dst.GenDecl: // 处理 server struct 注释
			if d.Tok != token.TYPE {
				continue
			}
			ts, ok := d.Specs[0].(*dst.TypeSpec)
			if !ok || ts.Name.Name != upper1st(service)+"Server" {
				continue
			}

			api := fmt.Sprintf(
				"%sServer 实现 /%s.v%s.%s 服务",
				upper1st(service),
				server,
				version,
				upper1st(service),
			)
			if comment := comments[upper1st(service)]; comment != nil {
				d.Decs.Start.Replace("// " + api + "\n")
				for _, c := range comment.List {
					d.Decs.Start.Append(c.Text)
				}
			}
		case *dst.FuncDecl: // 函数处理注释
			api := fmt.Sprintf(
				"%s 实现 /%s.v%s.%s/%s 接口",
				d.Name.Name,
				server,
				version,
				upper1st(service),
				d.Name.Name,
			)

			if comment, ok := comments[d.Name.Name]; comment != nil {
				d.Decs.Start.Replace("// " + api + "\n")
				for _, c := range comment.List {
					d.Decs.Start.Append(c.Text)
				}
			} else if !ok {
				if d.Recv != nil && d.Name.IsExported() && d.Name.Name != "Hooks" {
					// 删除 proto 中不存在的方法
					st, ok := d.Recv.List[0].Type.(*dst.StarExpr)
					if ok {
						x, ok := st.X.(*dst.Ident)
						if ok && x.Name == upper1st(service)+"Server" {
							decls = decls[:len(decls)-1]
						}
					}
				}
			}
		}
	}

	f.Decls = decls

	fb, err := os.OpenFile(serverFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		panic(err)
	}
	defer fb.Close()

	decorator.Fprint(fb, f)
}

func getComments(d *ast.GenDecl) map[string]*ast.CommentGroup {
	comments := make(map[string]*ast.CommentGroup)
	// rpc service注释单独添加
	if d.Doc != nil {
		comments[upper1st(service)] = d.Doc
	}

	for _, s := range d.Specs {
		ts, ok := s.(*ast.TypeSpec)
		if !ok {
			continue
		}

		it, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			continue
		}

		for _, method := range it.Methods.List {
			name := method.Names[0].Name
			if name == "Do" || name == "ServiceDescriptor" ||
				name == "ProtocGenTwirpVersion" {
				continue
			}

			comments[name] = method.Doc
		}
	}

	return comments
}

func appendFuncs(d *ast.GenDecl) {
	buf := &bytes.Buffer{}

	definedFuncs := scanDefinedFuncs(serverFile)

	for _, s := range d.Specs {
		ts, ok := s.(*ast.TypeSpec)
		if !ok {
			continue
		}

		it, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			continue
		}

		for _, m := range it.Methods.List {
			name := m.Names[0].Name
			if name == "Do" || name == "ServiceDescriptor" ||
				name == "ProtocGenTwirpVersion" {
				continue
			}

			if definedFuncs[name] {
				continue
			}

			appendFunc(buf, m)
		}
	}

	if buf.Len() == 0 {
		return
	}

	f, err := os.OpenFile(serverFile, os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	_, err = f.Write(buf.Bytes())

	if err != nil {
		panic(err)
	}
}

func appendFunc(buf *bytes.Buffer, f *ast.Field) {
	args := &funcTpl{}

	args.Name = f.Names[0].Name

	ft := f.Type.(*ast.FuncType)
	// 如果使用导入的 message 作为入参或出参，生成的代码会有语法错误！
	// 但处理这类情况比较复杂，这类用法也比较少，先不处理。
	// 先尽量使用自定义消息吧。
	args.ReqType = ft.Params.List[1].Type.(*ast.StarExpr).X.(*ast.Ident).Name
	args.RespType = ft.Results.List[0].Type.(*ast.StarExpr).X.(*ast.Ident).Name
	args.Service = upper1st(service)

	t, err := template.New("server").Parse(args.tpl())
	if err != nil {
		panic(err)
	}

	if err := t.Execute(buf, args); err != nil {
		panic(err)
	}
}

func scanDefinedFuncs(file string) map[string]bool {
	r, _ := parseAST(file)
	fs := make(map[string]bool)
	for _, decl := range r.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok {
			fs[f.Name.Name] = true
		}
	}

	return fs
}

// 判断文件是否存在
func fileExists(file string) bool {
	fd, err := os.Open(file)
	defer fd.Close()

	if err != nil && os.IsNotExist(err) {
		return false
	}
	return true
}
