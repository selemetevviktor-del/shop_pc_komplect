package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq" // драйвер PostgreSQL
)

// ---------- Сущности (соответствуют таблицам в db/shop_db.sql) ----------

type Category struct {
	ID   int
	Name string
}

type Product struct {
	ID          int
	Name        string
	CategoryID  int
	Price       int
	Stock       int
	Description string
}

type OrderItem struct {
	ProductID int
	Name      string
	Price     int
	Qty       int
}

type Order struct {
	ID        int
	FullName  string
	Phone     string
	City      string
	Address   string
	Items     []OrderItem
	Total     int
	CreatedAt time.Time
}

// ---------- Представления для шаблонов ----------

type ProductView struct {
	Product
	Category string
}

type CartLine struct {
	Product Product
	Qty     int
	Sum     int
}

type Cart struct {
	Lines []CartLine
	Total int
}

// ---------- Подключение к PostgreSQL ----------

type Store struct {
	db *sql.DB
}

// ErrUser - ошибка, текст которой можно показать пользователю.
type ErrUser struct{ Msg string }

func (e ErrUser) Error() string { return e.Msg }

func userErr(format string, a ...any) error { return ErrUser{fmt.Sprintf(format, a...)} }

func IsUserErr(err error) bool {
	var e ErrUser
	return errors.As(err, &e)
}

func OpenStore(dsn string) (*Store, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("не удалось подключиться к PostgreSQL: %w", err)
	}
	return &Store{db: db}, nil
}

// ---------- Чтение ----------

func (s *Store) Categories() []Category {
	rows, err := s.db.Query(`SELECT category_id, name FROM categories ORDER BY category_id`)
	if err != nil {
		log.Println("категории:", err)
		return nil
	}
	defer rows.Close()
	var res []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name); err == nil {
			res = append(res, c)
		}
	}
	return res
}

const productSelect = `
	SELECT p.product_id, p.name, p.category_id, c.name, p.price, p.stock, COALESCE(p.description, '')
	FROM products p JOIN categories c ON c.category_id = p.category_id `

func scanProduct(row interface{ Scan(...any) error }) (ProductView, error) {
	var p ProductView
	err := row.Scan(&p.ID, &p.Name, &p.CategoryID, &p.Category, &p.Price, &p.Stock, &p.Description)
	return p, err
}

// Products возвращает товары; categoryID = 0 - все категории.
func (s *Store) Products(categoryID int) []ProductView {
	rows, err := s.db.Query(productSelect+`WHERE ($1 = 0 OR p.category_id = $1) ORDER BY p.product_id`, categoryID)
	if err != nil {
		log.Println("товары:", err)
		return nil
	}
	defer rows.Close()
	var res []ProductView
	for rows.Next() {
		if p, err := scanProduct(rows); err == nil {
			res = append(res, p)
		}
	}
	return res
}

func (s *Store) Product(id int) (ProductView, bool) {
	p, err := scanProduct(s.db.QueryRow(productSelect+`WHERE p.product_id = $1`, id))
	return p, err == nil
}

// Cart превращает {id товара: количество} в строки корзины с ценами.
func (s *Store) Cart(items map[int]int) Cart {
	var ids []int
	for id, q := range items {
		if q > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	var c Cart
	for _, id := range ids {
		p, ok := s.Product(id)
		if !ok {
			continue // товар удалён из каталога
		}
		line := CartLine{p.Product, items[id], p.Price * items[id]}
		c.Lines = append(c.Lines, line)
		c.Total += line.Sum
	}
	return c
}

func (s *Store) orderItems(orderID int) []OrderItem {
	rows, err := s.db.Query(`SELECT COALESCE(product_id, 0), name, price, qty
		FROM order_items WHERE order_id = $1 ORDER BY item_id`, orderID)
	if err != nil {
		log.Println("состав заказа:", err)
		return nil
	}
	defer rows.Close()
	var res []OrderItem
	for rows.Next() {
		var it OrderItem
		if err := rows.Scan(&it.ProductID, &it.Name, &it.Price, &it.Qty); err == nil {
			res = append(res, it)
		}
	}
	return res
}

const orderSelect = `SELECT order_id, full_name, phone, city, address, total, created_at FROM orders `

func scanOrder(row interface{ Scan(...any) error }) (Order, error) {
	var o Order
	err := row.Scan(&o.ID, &o.FullName, &o.Phone, &o.City, &o.Address, &o.Total, &o.CreatedAt)
	return o, err
}

func (s *Store) Orders() []Order {
	rows, err := s.db.Query(orderSelect + `ORDER BY order_id DESC`)
	if err != nil {
		log.Println("заказы:", err)
		return nil
	}
	var res []Order
	for rows.Next() {
		if o, err := scanOrder(rows); err == nil {
			res = append(res, o)
		}
	}
	rows.Close()
	for i := range res {
		res[i].Items = s.orderItems(res[i].ID)
	}
	return res
}

func (s *Store) Order(id int) (Order, bool) {
	o, err := scanOrder(s.db.QueryRow(orderSelect+`WHERE order_id = $1`, id))
	if err != nil {
		return Order{}, false
	}
	o.Items = s.orderItems(o.ID)
	return o, true
}

// ---------- Бизнес-процесс 1: корзина и оформление заказа ----------

// CheckCart проверяет, что нужное количество товара есть на складе.
func (s *Store) CheckCart(productID, qty int) error {
	var name string
	var stock int
	err := s.db.QueryRow(`SELECT name, stock FROM products WHERE product_id = $1`, productID).Scan(&name, &stock)
	if errors.Is(err, sql.ErrNoRows) {
		return userErr("Товар не найден.")
	}
	if err != nil {
		return err
	}
	if qty > stock {
		return userErr("Товара «%s» на складе только %d шт.", name, stock)
	}
	return nil
}

// CreateOrder проверяет данные покупателя и остатки, списывает товар со склада
// и сохраняет заказ. Всё выполняется в одной транзакции: если что-то не удалось,
// не сохраняется ничего.
func (s *Store) CreateOrder(fullName, phone, city, address string, items map[int]int) (int, error) {
	fullName, phone = strings.TrimSpace(fullName), strings.TrimSpace(phone)
	city, address = strings.TrimSpace(city), strings.TrimSpace(address)
	if fullName == "" || phone == "" || city == "" || address == "" {
		return 0, userErr("Заполните ФИО, телефон, город и адрес.")
	}
	if len(items) == 0 {
		return 0, userErr("Корзина пуста.")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // после успешного Commit ничего не делает

	var ids []int
	for id, q := range items {
		if q > 0 {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)

	var lines []OrderItem
	total := 0
	for _, id := range ids {
		var name string
		var price, stock int
		// FOR UPDATE блокирует строку, пока идёт оформление заказа
		err := tx.QueryRow(`SELECT name, price, stock FROM products WHERE product_id = $1 FOR UPDATE`, id).
			Scan(&name, &price, &stock)
		if errors.Is(err, sql.ErrNoRows) {
			continue // товар удалён из каталога
		}
		if err != nil {
			return 0, err
		}
		if items[id] > stock {
			return 0, userErr("Товара «%s» осталось только %d шт. Измените корзину.", name, stock)
		}
		if _, err := tx.Exec(`UPDATE products SET stock = stock - $1 WHERE product_id = $2`, items[id], id); err != nil {
			return 0, err
		}
		lines = append(lines, OrderItem{id, name, price, items[id]})
		total += price * items[id]
	}
	if len(lines) == 0 {
		return 0, userErr("Товары из корзины больше не продаются.")
	}

	var orderID int
	err = tx.QueryRow(`INSERT INTO orders (full_name, phone, city, address, total)
		VALUES ($1, $2, $3, $4, $5) RETURNING order_id`, fullName, phone, city, address, total).Scan(&orderID)
	if err != nil {
		return 0, err
	}
	for _, it := range lines {
		_, err := tx.Exec(`INSERT INTO order_items (order_id, product_id, name, price, qty)
			VALUES ($1, $2, $3, $4, $5)`, orderID, it.ProductID, it.Name, it.Price, it.Qty)
		if err != nil {
			return 0, err
		}
	}
	return orderID, tx.Commit()
}

// ---------- Бизнес-процесс 2: управление товарами (админ-панель) ----------

func (s *Store) AddProduct(name string, categoryID, price, stock int, description string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 150 {
		return userErr("Название обязательно (до 150 символов).")
	}
	if price <= 0 {
		return userErr("Цена должна быть больше нуля.")
	}
	if stock < 0 {
		return userErr("Количество не может быть отрицательным.")
	}
	var exists bool
	if err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM categories WHERE category_id = $1)`, categoryID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return userErr("Выберите категорию.")
	}
	_, err := s.db.Exec(`INSERT INTO products (name, category_id, price, stock, description)
		VALUES ($1, $2, $3, $4, $5)`, name, categoryID, price, stock, strings.TrimSpace(description))
	return err
}

func (s *Store) SetStock(id, stock int) error {
	if stock < 0 {
		return userErr("Количество не может быть отрицательным.")
	}
	res, err := s.db.Exec(`UPDATE products SET stock = $1 WHERE product_id = $2`, stock, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return userErr("Товар не найден.")
	}
	return nil
}

func (s *Store) DeleteProduct(id int) error {
	res, err := s.db.Exec(`DELETE FROM products WHERE product_id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return userErr("Товар не найден.")
	}
	return nil
}
