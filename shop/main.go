package main

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type NavItem struct {
	Href, Label, Key string
}

var publicNav = []NavItem{
	{"/", "Главная", "home"},
	{"/products", "Каталог", "products"},
	{"/cart", "Корзина", "cart"},
}

var adminNav = []NavItem{
	{"/admin", "Товары", "admin"},
	{"/admin/orders", "Заказы", "orders"},
}

type Page struct {
	Title   string
	Active  string
	IsAdmin bool
	Nav     []NavItem
	Msg     string
	Err     string
	Next    string
	Data    any
}

type App struct {
	store    *Store
	tmpl     map[string]*template.Template
	password string

	mu       sync.Mutex
	sessions map[string]time.Time   
	carts    map[string]map[int]int 

func main() {
	addr := flag.String("addr", env("SHOP_ADDR", ":8080"), "адрес сервера")
	flag.Parse()

	password := os.Getenv("SHOP_PASSWORD")
	if password == "" {
		password = "admin"
		log.Println("ВНИМАНИЕ: пароль администратора по умолчанию - «admin». Задайте SHOP_PASSWORD.")
	}

	store, err := OpenStore(dbURL())
	if err != nil {
		log.Fatal(err)
	}
	app := &App{
		store: store, password: password,
		sessions: map[string]time.Time{}, carts: map[string]map[int]int{},
	}
	if err := app.loadTemplates("templates"); err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           app.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	log.Printf("Магазин запущен: http://localhost%s", *addr)
	log.Fatal(srv.ListenAndServe())
}

// dbURL собирает строку подключения к PostgreSQL из переменных окружения.
func dbURL() string {
	password := os.Getenv("SHOP_DB_PASSWORD")
	if password == "" {
		log.Fatal("Задайте пароль PostgreSQL в переменной SHOP_DB_PASSWORD (см. README).")
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(env("SHOP_DB_USER", "postgres"), password),
		Host:     env("SHOP_DB_HOST", "localhost") + ":" + env("SHOP_DB_PORT", "5432"),
		Path:     env("SHOP_DB_NAME", "shop"),
		RawQuery: "sslmode=disable",
	}
	return u.String()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (a *App) loadTemplates(dir string) error {
	pages := []string{
		"home", "products", "product", "cart", "order_new", "order_done",
		"login", "admin", "admin_new", "admin_orders", "error",
	}
	funcs := template.FuncMap{
		"date": func(t time.Time) string { return t.Format("02.01.2006 15:04") },
	}
	a.tmpl = map[string]*template.Template{}
	for _, p := range pages {
		t, err := template.New(p).Funcs(funcs).ParseFiles(
			filepath.Join(dir, "base.html"), filepath.Join(dir, p+".html"))
		if err != nil {
			return fmt.Errorf("шаблон %s: %w", p, err)
		}
		a.tmpl[p] = t
	}
	return nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	// покупатель
	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /products", a.products)
	mux.HandleFunc("GET /products/{id}", a.product)
	mux.HandleFunc("GET /cart", a.cart)
	mux.HandleFunc("POST /cart/add", a.cartAdd)
	mux.HandleFunc("POST /cart/remove/{id}", a.cartRemove)
	mux.HandleFunc("GET /order/new", a.orderForm)
	mux.HandleFunc("POST /order/new", a.orderNew)
	mux.HandleFunc("GET /order/{id}", a.orderDone)

	// вход администратора
	mux.HandleFunc("GET /login", a.loginForm)
	mux.HandleFunc("POST /login", a.login)
	mux.HandleFunc("POST /logout", a.logout)

	// админ-панель
	mux.HandleFunc("GET /admin", a.admin(a.adminProducts))
	mux.HandleFunc("GET /admin/new", a.admin(a.adminNewForm))
	mux.HandleFunc("POST /admin/new", a.admin(a.adminNew))
	mux.HandleFunc("POST /admin/{id}/stock", a.admin(a.adminStock))
	mux.HandleFunc("POST /admin/{id}/delete", a.admin(a.adminDelete))
	mux.HandleFunc("GET /admin/orders", a.admin(a.adminOrders))

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.render(w, r, "error", "Ошибка", "", "Страница не найдена.", "", http.StatusNotFound)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r)
	})
}

// ---------- Вспомогательные функции ----------

func (a *App) render(w http.ResponseWriter, r *http.Request, name, title, active string, data any, errText string, status int) {
	isAdmin := a.isAdmin(r)
	nav := publicNav
	if isAdmin {
		nav = append(append([]NavItem{}, publicNav...), adminNav...)
	}
	if errText == "" {
		errText = r.URL.Query().Get("err")
	}
	p := Page{
		Title: title, Active: active, IsAdmin: isAdmin, Nav: nav,
		Msg: r.URL.Query().Get("msg"), Err: errText, Next: r.URL.Path, Data: data,
	}
	var buf bytes.Buffer
	if err := a.tmpl[name].ExecuteTemplate(&buf, "base", p); err != nil {
		log.Printf("шаблон %s: %v", name, err)
		http.Error(w, "Внутренняя ошибка сервера", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func redirect(w http.ResponseWriter, r *http.Request, to, msg, errText string) {
	q := url.Values{}
	if msg != "" {
		q.Set("msg", msg)
	}
	if errText != "" {
		q.Set("err", errText)
	}
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		to += sep + q.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func msgOf(err error) string {
	if IsUserErr(err) {
		return err.Error()
	}
	log.Println("ошибка:", err)
	return "Внутренняя ошибка. Попробуйте ещё раз позже."
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func pathID(r *http.Request) int { return atoi(r.PathValue("id")) }

func newToken() string {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// ---------- Корзина (хранится на сервере, привязана к cookie) ----------

const cartCookie = "shop_cart"

// cartItems возвращает корзину покупателя; если её ещё нет - создаёт.
func (a *App) cartItems(w http.ResponseWriter, r *http.Request) map[int]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, err := r.Cookie(cartCookie); err == nil {
		if items, ok := a.carts[c.Value]; ok {
			return items
		}
	}
	token := newToken()
	items := map[int]int{}
	a.carts[token] = items
	http.SetCookie(w, &http.Cookie{
		Name: cartCookie, Value: token, Path: "/", MaxAge: 7 * 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return items
}

// ---------- Вход администратора ----------

const sessionCookie = "shop_admin"

func (a *App) isAdmin(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.sessions[c.Value]
	if !ok || time.Now().After(exp) {
		delete(a.sessions, c.Value)
		return false
	}
	return true
}

func (a *App) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.isAdmin(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

func (a *App) loginForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "login", "Вход для администратора", "", nil, "", http.StatusOK)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.FormValue("password")), []byte(a.password)) != 1 {
		time.Sleep(500 * time.Millisecond)
		redirect(w, r, "/login", "", "Неверный пароль.")
		return
	}
	token := newToken()
	a.mu.Lock()
	a.sessions[token] = time.Now().Add(8 * time.Hour)
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: 8 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	redirect(w, r, "/", "Вы вышли из админ-панели.", "")
}

// ---------- Страницы покупателя ----------

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "home", "Главная", "home", a.store.Categories(), "", http.StatusOK)
}

func (a *App) products(w http.ResponseWriter, r *http.Request) {
	cat := atoi(r.URL.Query().Get("category"))
	a.render(w, r, "products", "Каталог", "products", map[string]any{
		"Products": a.store.Products(cat), "Categories": a.store.Categories(), "Category": cat,
	}, "", http.StatusOK)
}

func (a *App) product(w http.ResponseWriter, r *http.Request) {
	p, ok := a.store.Product(pathID(r))
	if !ok {
		a.render(w, r, "error", "Ошибка", "", "Товар не найден.", "", http.StatusNotFound)
		return
	}
	a.render(w, r, "product", p.Name, "products", p, "", http.StatusOK)
}

func (a *App) cart(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "cart", "Корзина", "cart", a.store.Cart(a.cartItems(w, r)), "", http.StatusOK)
}

// Бизнес-процесс 1, шаг 1: добавить товар в корзину.
func (a *App) cartAdd(w http.ResponseWriter, r *http.Request) {
	id, qty := atoi(r.FormValue("product")), atoi(r.FormValue("qty"))
	if qty < 1 {
		qty = 1
	}
	items := a.cartItems(w, r)
	a.mu.Lock()
	newQty := items[id] + qty
	a.mu.Unlock()
	if err := a.store.CheckCart(id, newQty); err != nil {
		redirect(w, r, "/products/"+strconv.Itoa(id), "", msgOf(err))
		return
	}
	a.mu.Lock()
	items[id] = newQty
	a.mu.Unlock()
	redirect(w, r, "/cart", "Товар добавлен в корзину.", "")
}

func (a *App) cartRemove(w http.ResponseWriter, r *http.Request) {
	items := a.cartItems(w, r)
	a.mu.Lock()
	delete(items, pathID(r))
	a.mu.Unlock()
	redirect(w, r, "/cart", "Товар убран из корзины.", "")
}

// Бизнес-процесс 1, шаг 2: оформить заказ из корзины.
func (a *App) orderForm(w http.ResponseWriter, r *http.Request) {
	cart := a.store.Cart(a.cartItems(w, r))
	if len(cart.Lines) == 0 {
		redirect(w, r, "/cart", "", "Корзина пуста.")
		return
	}
	a.render(w, r, "order_new", "Оформление заказа", "cart",
		map[string]any{"Cart": cart, "F": map[string]string{}}, "", http.StatusOK)
}

func (a *App) orderNew(w http.ResponseWriter, r *http.Request) {
	items := a.cartItems(w, r)
	f := map[string]string{
		"FullName": r.FormValue("full_name"), "Phone": r.FormValue("phone"),
		"City": r.FormValue("city"), "Address": r.FormValue("address"),
	}
	a.mu.Lock()
	snapshot := map[int]int{}
	for id, q := range items {
		snapshot[id] = q
	}
	a.mu.Unlock()

	id, err := a.store.CreateOrder(f["FullName"], f["Phone"], f["City"], f["Address"], snapshot)
	if err != nil {
		a.render(w, r, "order_new", "Оформление заказа", "cart",
			map[string]any{"Cart": a.store.Cart(snapshot), "F": f}, msgOf(err), http.StatusUnprocessableEntity)
		return
	}
	a.mu.Lock()
	for k := range items {
		delete(items, k)
	}
	a.mu.Unlock()
	redirect(w, r, "/order/"+strconv.Itoa(id), "", "")
}

func (a *App) orderDone(w http.ResponseWriter, r *http.Request) {
	o, ok := a.store.Order(pathID(r))
	if !ok {
		a.render(w, r, "error", "Ошибка", "", "Заказ не найден.", "", http.StatusNotFound)
		return
	}
	a.render(w, r, "order_done", "Заказ оформлен", "cart", o, "", http.StatusOK)
}

// ---------- Админ-панель (бизнес-процесс 2) ----------

func (a *App) adminProducts(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "admin", "Управление товарами", "admin", a.store.Products(0), "", http.StatusOK)
}

func (a *App) adminNewForm(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "admin_new", "Новый товар", "admin", map[string]any{
		"Categories": a.store.Categories(), "F": map[string]string{"Stock": "1"},
	}, "", http.StatusOK)
}

func (a *App) adminNew(w http.ResponseWriter, r *http.Request) {
	f := map[string]string{
		"Name": r.FormValue("name"), "Category": r.FormValue("category"), "Price": r.FormValue("price"),
		"Stock": r.FormValue("stock"), "Description": r.FormValue("description"),
	}
	err := a.store.AddProduct(f["Name"], atoi(f["Category"]), atoi(f["Price"]), atoi(f["Stock"]), f["Description"])
	if err != nil {
		a.render(w, r, "admin_new", "Новый товар", "admin", map[string]any{
			"Categories": a.store.Categories(), "F": f,
		}, msgOf(err), http.StatusUnprocessableEntity)
		return
	}
	redirect(w, r, "/admin", "Товар добавлен.", "")
}

func (a *App) adminStock(w http.ResponseWriter, r *http.Request) {
	if err := a.store.SetStock(pathID(r), atoi(r.FormValue("stock"))); err != nil {
		redirect(w, r, "/admin", "", msgOf(err))
		return
	}
	redirect(w, r, "/admin", "Количество изменено.", "")
}

func (a *App) adminDelete(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteProduct(pathID(r)); err != nil {
		redirect(w, r, "/admin", "", msgOf(err))
		return
	}
	redirect(w, r, "/admin", "Товар удалён.", "")
}

func (a *App) adminOrders(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "admin_orders", "Заказы", "orders", a.store.Orders(), "", http.StatusOK)
}
