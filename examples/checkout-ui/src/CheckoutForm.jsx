import React, { useState } from 'react';

export default function CheckoutForm({ onSubmit }) {
  const [form, setForm] = useState({ email: '', address: '' });

  const handleChange = (e) => {
    setForm({ ...form, [e.target.name]: e.target.value });
  };

  const handleSubmit = (e) => {
    e.preventDefault();
    onSubmit(form);
  };

  return (
    <form onSubmit={handleSubmit}>
      <input name="email" value={form.email} onChange={handleChange} placeholder="Email" />
      <input name="address" value={form.address} onChange={handleChange} placeholder="Address" />
      <button type="submit">Place Order</button>
    </form>
  );
}
