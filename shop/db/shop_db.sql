-- База данных магазина компьютерных комплектующих (PostgreSQL)
--
-- Запуск в psql:      \i 'C:/путь/до/shop_db.sql'
-- Запуск из консоли:  psql -U postgres -f shop_db.sql
--
-- Скрипт сам создаёт базу shop. Если запускаете в pgAdmin, то создайте базу
-- shop вручную, выберите её и выполните только часть ниже строки "ТАБЛИЦЫ".

DROP DATABASE IF EXISTS shop;
CREATE DATABASE shop ENCODING 'UTF8';
\connect shop

-- ТАБЛИЦЫ ---------------------------------------------------------------

CREATE TABLE categories (
    category_id  SERIAL       PRIMARY KEY,
    name         VARCHAR(100) NOT NULL UNIQUE
);

CREATE TABLE products (
    product_id   SERIAL       PRIMARY KEY,
    name         VARCHAR(150) NOT NULL,
    category_id  INTEGER      NOT NULL REFERENCES categories (category_id),
    price        INTEGER      NOT NULL CHECK (price > 0),
    stock        INTEGER      NOT NULL DEFAULT 0 CHECK (stock >= 0),
    description  TEXT
);

CREATE TABLE orders (
    order_id    SERIAL       PRIMARY KEY,
    full_name   VARCHAR(150) NOT NULL,
    phone       VARCHAR(30)  NOT NULL,
    city        VARCHAR(100) NOT NULL,
    address     VARCHAR(200) NOT NULL,
    total       INTEGER      NOT NULL CHECK (total >= 0),
    created_at  TIMESTAMP    NOT NULL DEFAULT now()
);

-- Название и цена копируются в строку заказа, поэтому удаление товара
-- из каталога не портит старые заказы (product_id тогда становится NULL).
CREATE TABLE order_items (
    item_id     SERIAL       PRIMARY KEY,
    order_id    INTEGER      NOT NULL REFERENCES orders (order_id) ON DELETE CASCADE,
    product_id  INTEGER      REFERENCES products (product_id) ON DELETE SET NULL,
    name        VARCHAR(150) NOT NULL,
    price       INTEGER      NOT NULL CHECK (price > 0),
    qty         INTEGER      NOT NULL CHECK (qty > 0)
);

CREATE INDEX ix_products_category ON products (category_id);
CREATE INDEX ix_order_items_order ON order_items (order_id);

-- ДЕМО-ДАННЫЕ -----------------------------------------------------------

INSERT INTO categories (name) VALUES
    ('Процессоры'), ('Видеокарты'), ('Оперативная память'),
    ('Материнские платы'), ('Накопители');

INSERT INTO products (name, category_id, price, stock, description) VALUES
    ('Intel Core i5-12400F',          1, 12500, 10, '6 ядер, сокет LGA1700'),
    ('AMD Ryzen 5 5600',              1, 11900,  8, '6 ядер, сокет AM4'),
    ('NVIDIA GeForce RTX 4060',       2, 32900,  5, '8 ГБ GDDR6'),
    ('AMD Radeon RX 7600',            2, 28900,  4, '8 ГБ GDDR6'),
    ('Kingston Fury 16 ГБ DDR4',      3,  3900, 20, '2x8 ГБ, 3200 МГц'),
    ('Corsair Vengeance 32 ГБ DDR5',  3,  9900, 12, '2x16 ГБ, 6000 МГц'),
    ('MSI B660M-A',                   4,  9800,  6, 'mATX, LGA1700, DDR4'),
    ('ASUS PRIME B550-PLUS',          4, 10500,  7, 'ATX, AM4, DDR4'),
    ('Samsung 980 500 ГБ',            5,  4800, 15, 'SSD NVMe M.2'),
    ('Seagate Barracuda 2 ТБ',        5,  4900,  9, 'HDD 3.5 дюйма, 7200 об/мин');

-- ПРИМЕРЫ ЗАПРОСОВ (то, что будет делать сайт) -----------------------------
--
-- Каталог:               SELECT p.*, c.name FROM products p JOIN categories c USING (category_id);
-- Добавить товар:        INSERT INTO products (name, category_id, price, stock) VALUES ('Новый', 1, 5000, 3);
-- Изменить количество:   UPDATE products SET stock = 20 WHERE product_id = 1;
-- Удалить товар:         DELETE FROM products WHERE product_id = 1;
--
-- Оформление заказа (одной транзакцией):
--   BEGIN;
--   INSERT INTO orders (full_name, phone, city, address, total)
--          VALUES ('Иванов Иван', '+79001112233', 'Москва', 'ул. Ленина 1', 65800) RETURNING order_id;
--   INSERT INTO order_items (order_id, product_id, name, price, qty)
--          VALUES (1, 3, 'NVIDIA GeForce RTX 4060', 32900, 2);
--   UPDATE products SET stock = stock - 2 WHERE product_id = 3 AND stock >= 2;
--   COMMIT;
