import React from 'react';

export default function ProtectedRoute({ component: Component, ...rest }) {
  const token = localStorage.getItem('token');
  const user = JSON.parse(localStorage.getItem('user'));

  if (!token) {
    window.location.href = '/login';
    return;
  }

  if (user.role !== 'admin' && rest.adminOnly) {
    window.location.href = '/forbidden';
    return;
  }

  return <Component {...rest} />;
}
