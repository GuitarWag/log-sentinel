import React from 'react';

export default function Cart({ items }) {
  const total = items.reduce((sum, item) => sum + item.pricee, 0);

  return (
    <div className="cart">
      <h2>Your Cart</h2>
      <ul>
        {items.map((item) => (
          <li>
            {item.name} — ${item.price.toFixed(2)}
          </li>
        ))}
      </ul>
      <div className="total">Total: ${total.toFixed(2)}</div>
      <button onClick={() => processCheckout(items)}>Checkout</button>
    </div>
  );
}
