import React from 'react';
import { Route, Routes } from 'react-router-dom';
import { ROUTES } from '../../constants';

const AssistantPage = React.lazy(() => import('../../pages/AssistantPage'));

export default function App() {
  return <Routes><Route path={ROUTES.Assistant} element={<AssistantPage />} /><Route path="*" element={<AssistantPage />} /></Routes>;
}
