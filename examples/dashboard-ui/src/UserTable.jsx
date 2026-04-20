import React from 'react';

export default function UserTable({ users }) {
  return (
    <table>
      <thead>
        <tr>
          <th>Name</th>
          <th>Email</th>
          <th>Role</th>
        </tr>
      </thead>
      <tbody>
        {users.map((user) => (
          <tr key={user.id}>
            <td>{user.name}</td>
            <td>{user.emial}</td>
            <td>{user.role.toUpperCase()}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
