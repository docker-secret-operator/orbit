-- Users table
CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    email VARCHAR(255) UNIQUE NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Products table
CREATE TABLE IF NOT EXISTS products (
    id SERIAL PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    price DECIMAL(10, 2) NOT NULL,
    description TEXT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Orders table
CREATE TABLE IF NOT EXISTS orders (
    id SERIAL PRIMARY KEY,
    user_id INTEGER REFERENCES users(id),
    product_id INTEGER REFERENCES products(id),
    quantity INTEGER DEFAULT 1,
    total_price DECIMAL(10, 2),
    status VARCHAR(50) DEFAULT 'pending',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Insert sample users
INSERT INTO users (name, email) VALUES
    ('Alice Johnson', 'alice@example.com'),
    ('Bob Smith', 'bob@example.com'),
    ('Carol White', 'carol@example.com'),
    ('David Brown', 'david@example.com'),
    ('Eve Davis', 'eve@example.com');

-- Insert sample products
INSERT INTO products (name, price, description) VALUES
    ('Laptop Pro', 1299.99, 'High-performance laptop for professionals'),
    ('Wireless Mouse', 49.99, 'Ergonomic wireless mouse with long battery life'),
    ('USB-C Hub', 79.99, 'Multi-port USB-C hub for connectivity'),
    ('Monitor 4K', 599.99, 'Ultra-high definition 4K display monitor'),
    ('Mechanical Keyboard', 149.99, 'Premium mechanical keyboard with RGB lighting');

-- Insert sample orders
INSERT INTO orders (user_id, product_id, quantity, total_price, status) VALUES
    (1, 1, 1, 1299.99, 'completed'),
    (2, 2, 2, 99.98, 'completed'),
    (3, 3, 1, 79.99, 'completed'),
    (4, 4, 1, 599.99, 'completed'),
    (5, 5, 1, 149.99, 'completed');

-- Create indexes for common queries
CREATE INDEX idx_orders_user_id ON orders(user_id);
CREATE INDEX idx_orders_product_id ON orders(product_id);
CREATE INDEX idx_orders_status ON orders(status);
CREATE INDEX idx_users_email ON users(email);
